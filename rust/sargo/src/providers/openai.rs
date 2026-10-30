//! OpenAI provider: Chat Completions API, `POST {base_url}/v1/chat/completions`.
//!
//! OpenAI's format differs from our neutral (Anthropic-like) one in three ways, handled here:
//! * the system prompt is a message with role "system";
//! * a tool call is a field `tool_calls` on the assistant message, and its arguments are a
//!   JSON *string* (ours is a JSON object);
//! * a tool result is its own message with role "tool" (ours is a block inside a "user" message).

use crate::error::RouterError;
use crate::config::{HttpClientConfig, ProviderConfig};
use crate::http_client::post_json;
use crate::json_util::{take_field, take_string};
use super::Provider;
use crate::types::{
    ContentBlock, InferenceRequest, InferenceResponse, Message, Usage, DEFAULT_MAX_TOKENS,
};
use serde_json::{json, Value};
use std::time::Duration;

pub struct OpenAiProvider {
    api_key: String,
    /// "https://api.openai.com" normally; any OpenAI-compatible server also works.
    base_url: String,
    /// How long to wait for the provider (from `[http_client]` in the config).
    timeout: Duration,
}

impl OpenAiProvider {
    pub fn new(api_key: &str, config: &ProviderConfig, http: &HttpClientConfig) -> OpenAiProvider {
        OpenAiProvider {
            api_key: api_key.to_string(),
            base_url: config.base_url.trim_end_matches('/').to_string(),
            timeout: Duration::from_secs(http.timeout_secs),
        }
    }
}

impl Provider for OpenAiProvider {
    fn name(&self) -> &str {
        "openai"
    }

    fn infer(&self, request: &InferenceRequest) -> Result<InferenceResponse, RouterError> {
        let body = build_request_body(request);
        let url = format!("{}/v1/chat/completions", self.base_url);
        let authorization = format!("Bearer {}", self.api_key);
        let headers = [("authorization", authorization.as_str())];
        let reply = post_json(&url, &headers, &body, self.timeout)?;
        parse_response(reply)
    }
}

/// Builds the JSON sent to OpenAI. Only reads `request` (borrowed).
fn build_request_body(request: &InferenceRequest) -> Value {
    // Our messages become a list of OpenAI messages (one of ours can become several).
    let mut messages: Vec<Value> = Vec::new();
    if let Some(system) = &request.system {
        messages.push(json!({"role": "system", "content": system}));
    }
    for message in &request.messages {
        if message.role == "assistant" {
            messages.push(assistant_message(message));
        } else {
            // `&mut messages`: lends the list out for appending; we get it back afterwards.
            push_user_messages(message, &mut messages);
        }
    }

    let mut body = json!({
        "model": request.model,
        // "max_completion_tokens" is accepted by all current chat models ("max_tokens" is not).
        "max_completion_tokens": request.max_tokens.unwrap_or(DEFAULT_MAX_TOKENS),
    });
    // Moves the Vec into the body (no copy).
    body["messages"] = Value::Array(messages);

    if let Some(temperature) = request.temperature {
        body["temperature"] = json!(temperature);
    }
    if !request.tools.is_empty() {
        let mut tools: Vec<Value> = Vec::new();
        for tool in &request.tools {
            tools.push(json!({
                "type": "function",
                "function": {
                    "name": tool.name,
                    "description": tool.description,
                    "parameters": tool.input_schema,
                }
            }));
        }
        body["tools"] = Value::Array(tools);
    }
    body
}

/// An assistant message: its text blocks become `content`, its `ToolUse` blocks become `tool_calls`.
fn assistant_message(message: &Message) -> Value {
    let mut text = String::new();
    let mut tool_calls: Vec<Value> = Vec::new();

    // `&message.content`: we only look at the blocks. `block` is a `&ContentBlock`, and the names
    // bound in each pattern (`id`, `name`, ...) are references into it, not copies.
    for block in &message.content {
        match block {
            ContentBlock::Text { text: part } => text.push_str(part),
            ContentBlock::ToolUse { id, name, input } => tool_calls.push(json!({
                "id": id,
                "type": "function",
                // OpenAI wants the arguments as a JSON *string*.
                "function": {"name": name, "arguments": input.to_string()},
            })),
            // A tool result inside an assistant message makes no sense: ignored.
            ContentBlock::ToolResult { .. } => {}
        }
    }

    let mut result = json!({"role": "assistant"});
    if text.is_empty() {
        result["content"] = Value::Null;
    } else {
        result["content"] = Value::String(text); // moves `text`
    }
    if !tool_calls.is_empty() {
        result["tool_calls"] = Value::Array(tool_calls); // moves `tool_calls`
    }
    result
}

/// A "user" message of ours becomes: one "tool" message per `ToolResult` block, followed by one
/// "user" message with all its text (if any). Tool messages must come right after the assistant
/// message that made the calls, so they are pushed first.
fn push_user_messages(message: &Message, out: &mut Vec<Value>) {
    let mut text = String::new();
    for block in &message.content {
        match block {
            ContentBlock::ToolResult {
                tool_use_id,
                content,
                is_error,
            } => {
                // OpenAI has no error flag on tool messages: say it in the text.
                let shown = if *is_error {
                    format!("Error: {}", content)
                } else {
                    content.to_string()
                };
                out.push(json!({"role": "tool", "tool_call_id": tool_use_id, "content": shown}));
            }
            ContentBlock::Text { text: part } => {
                if !text.is_empty() {
                    text.push('\n');
                }
                text.push_str(part);
            }
            // A tool call inside a user message makes no sense: ignored.
            ContentBlock::ToolUse { .. } => {}
        }
    }
    if !text.is_empty() {
        out.push(json!({"role": "user", "content": text}));
    }
}

/// Converts OpenAI's answer into our format.
///
/// MEMORY NOTE: like the Anthropic one, takes `response` by value and MOVES the pieces out of it
/// (`take_field` / `take_string`) instead of cloning them.
fn parse_response(mut response: Value) -> Result<InferenceResponse, RouterError> {
    let input_tokens = response["usage"]["prompt_tokens"].as_u64().unwrap_or(0) as u32;
    let output_tokens = response["usage"]["completion_tokens"].as_u64().unwrap_or(0) as u32;
    let model = take_string(take_field(&mut response, "model"));

    // We always ask for one answer, so we use the first "choice".
    let mut choice = match take_field(&mut response, "choices") {
        Value::Array(mut choices) if !choices.is_empty() => choices.remove(0),
        _ => return Err(RouterError::new(502, "openai reply has no choices")),
    };

    let stop_reason = match take_string(take_field(&mut choice, "finish_reason")).as_str() {
        "stop" => "end_turn".to_string(),
        "tool_calls" | "function_call" => "tool_use".to_string(),
        "length" => "max_tokens".to_string(),
        other => other.to_string(),
    };

    let mut message = take_field(&mut choice, "message");
    let mut content: Vec<ContentBlock> = Vec::new();

    // Text answer (null when the model only calls tools).
    if let Value::String(text) = take_field(&mut message, "content") {
        if !text.is_empty() {
            content.push(ContentBlock::Text { text });
        }
    }

    // Tool calls.
    if let Value::Array(calls) = take_field(&mut message, "tool_calls") {
        for mut call in calls {
            let id = take_string(take_field(&mut call, "id"));
            let mut function = take_field(&mut call, "function");
            let name = take_string(take_field(&mut function, "name"));
            let arguments = take_string(take_field(&mut function, "arguments"));

            // The arguments arrive as a JSON string; parse them into a real JSON value.
            let input: Value = if arguments.trim().is_empty() {
                json!({})
            } else {
                match serde_json::from_str(&arguments) {
                    Ok(parsed) => parsed,
                    // The model produced invalid JSON (it happens): hand the raw text to the
                    // caller, moving `arguments` into the Value instead of copying it.
                    Err(_) => Value::String(arguments),
                }
            };
            content.push(ContentBlock::ToolUse { id, name, input });
        }
    }

    Ok(InferenceResponse {
        provider: "openai".to_string(),
        model,
        content,
        stop_reason,
        usage: Usage {
            input_tokens,
            output_tokens,
        },
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_tool_call_reply() {
        let reply = json!({
            "model": "gpt-test",
            "choices": [{
                "finish_reason": "tool_calls",
                "message": {
                    "content": null,
                    "tool_calls": [{
                        "id": "call_1",
                        "type": "function",
                        "function": {"name": "weather", "arguments": "{\"city\":\"Oslo\"}"}
                    }]
                }
            }],
            "usage": {"prompt_tokens": 7, "completion_tokens": 3}
        });
        let parsed = parse_response(reply).unwrap();
        assert_eq!(parsed.stop_reason, "tool_use");
        assert_eq!(parsed.content.len(), 1);
        match &parsed.content[0] {
            ContentBlock::ToolUse { id, name, input } => {
                assert_eq!(id, "call_1");
                assert_eq!(name, "weather");
                assert_eq!(input["city"], "Oslo");
            }
            _ => panic!("expected a tool_use block"),
        }
    }

    #[test]
    fn tool_result_becomes_tool_message() {
        let request: InferenceRequest = serde_json::from_value(json!({
            "provider": "openai",
            "model": "gpt-test",
            "messages": [
                {"role": "user", "content": [{"type": "text", "text": "weather?"}]},
                {"role": "assistant", "content": [
                    {"type": "tool_use", "id": "call_1", "name": "weather", "input": {"city": "Oslo"}}
                ]},
                {"role": "user", "content": [
                    {"type": "tool_result", "tool_use_id": "call_1", "content": "rain"}
                ]}
            ]
        }))
        .unwrap();
        let body = build_request_body(&request);
        let messages = body["messages"].as_array().unwrap();
        assert_eq!(messages.len(), 3);
        assert_eq!(messages[1]["tool_calls"][0]["id"], "call_1");
        assert_eq!(messages[2]["role"], "tool");
        assert_eq!(messages[2]["tool_call_id"], "call_1");
    }
}
