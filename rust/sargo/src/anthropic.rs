//! Anthropic (Claude) provider: Messages API, `POST {base_url}/v1/messages`.
//!
//! Our neutral format (`types.rs`) is already Anthropic's format, so the request is almost a
//! direct serialization. The reply is converted back to our `InferenceResponse`.

use crate::error::RouterError;
use crate::http_client::post_json;
use crate::json_util::{take_field, take_string};
use crate::provider::Provider;
use crate::types::{ContentBlock, InferenceRequest, InferenceResponse, Usage, DEFAULT_MAX_TOKENS};
use serde_json::{json, Value};

pub struct AnthropicProvider {
    api_key: String,
    /// "https://api.anthropic.com" normally; overridable for tests or proxies.
    base_url: String,
}

impl AnthropicProvider {
    /// Takes `&str`s and stores owned copies, so the provider does not depend on where they came from.
    pub fn new(api_key: &str, base_url: &str) -> AnthropicProvider {
        AnthropicProvider {
            api_key: api_key.to_string(),
            base_url: base_url.trim_end_matches('/').to_string(),
        }
    }
}

impl Provider for AnthropicProvider {
    fn name(&self) -> &str {
        "anthropic"
    }

    fn infer(&self, request: &InferenceRequest) -> Result<InferenceResponse, RouterError> {
        let body = build_request_body(request);
        let url = format!("{}/v1/messages", self.base_url);
        let headers = [
            ("x-api-key", self.api_key.as_str()),
            ("anthropic-version", "2023-06-01"),
        ];
        let reply = post_json(&url, &headers, &body)?;
        parse_response(reply)
    }
}

/// Builds the JSON sent to Anthropic. Only reads `request` (borrowed).
/// (`json!` serializes the values it is given by reference, so `request` is not moved or changed.)
fn build_request_body(request: &InferenceRequest) -> Value {
    let mut body = json!({
        "model": request.model,
        "max_tokens": request.max_tokens.unwrap_or(DEFAULT_MAX_TOKENS),
        // `Message` and `ContentBlock` derive Serialize in exactly Anthropic's shape.
        "messages": request.messages,
    });

    if let Some(system) = &request.system {
        body["system"] = json!(system);
    }
    if let Some(temperature) = request.temperature {
        body["temperature"] = json!(temperature);
    }
    if !request.tools.is_empty() {
        let mut tools: Vec<Value> = Vec::new();
        for tool in &request.tools {
            tools.push(json!({
                "name": tool.name,
                "description": tool.description,
                "input_schema": tool.input_schema,
            }));
        }
        // Moves the Vec into the body (no copy).
        body["tools"] = Value::Array(tools);
    }
    body
}

/// Converts Anthropic's answer into our format.
///
/// MEMORY NOTE: takes `response` BY VALUE (ownership moves in) and then moves the strings and
/// JSON out of it with `take_field`/`take_string`, so nothing is cloned. After this function the
/// original `response` no longer exists, which is fine, nobody needs it.
fn parse_response(mut response: Value) -> Result<InferenceResponse, RouterError> {
    // Reading numbers only borrows `response`; `u64` is `Copy` so nothing is moved.
    let input_tokens = response["usage"]["input_tokens"].as_u64().unwrap_or(0) as u32;
    let output_tokens = response["usage"]["output_tokens"].as_u64().unwrap_or(0) as u32;

    let model = take_string(take_field(&mut response, "model"));
    let stop_reason = match take_string(take_field(&mut response, "stop_reason")).as_str() {
        "end_turn" | "stop_sequence" => "end_turn".to_string(),
        "tool_use" => "tool_use".to_string(),
        "max_tokens" => "max_tokens".to_string(),
        other => other.to_string(),
    };

    let blocks = match take_field(&mut response, "content") {
        Value::Array(blocks) => blocks,
        _ => return Err(RouterError::new(502, "anthropic reply has no content array")),
    };

    let mut content: Vec<ContentBlock> = Vec::new();
    // `for block in blocks` consumes the Vec: each `block` is owned by one loop turn.
    for mut block in blocks {
        let kind = take_string(take_field(&mut block, "type"));
        match kind.as_str() {
            "text" => content.push(ContentBlock::Text {
                text: take_string(take_field(&mut block, "text")),
            }),
            "tool_use" => content.push(ContentBlock::ToolUse {
                id: take_string(take_field(&mut block, "id")),
                name: take_string(take_field(&mut block, "name")),
                input: take_field(&mut block, "input"),
            }),
            // Other block types (e.g. "thinking") are not part of sargo's format: skipped.
            _ => {}
        }
    }

    Ok(InferenceResponse {
        provider: "anthropic".to_string(),
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
    fn parses_tool_use_reply() {
        let reply = json!({
            "model": "claude-test",
            "stop_reason": "tool_use",
            "content": [
                {"type": "text", "text": "Let me check."},
                {"type": "tool_use", "id": "tu_1", "name": "weather", "input": {"city": "Oslo"}}
            ],
            "usage": {"input_tokens": 10, "output_tokens": 5}
        });
        let parsed = parse_response(reply).unwrap();
        assert_eq!(parsed.stop_reason, "tool_use");
        assert_eq!(parsed.content.len(), 2);
        assert_eq!(parsed.usage.input_tokens, 10);
        match &parsed.content[1] {
            ContentBlock::ToolUse { id, name, input } => {
                assert_eq!(id, "tu_1");
                assert_eq!(name, "weather");
                assert_eq!(input["city"], "Oslo");
            }
            _ => panic!("expected a tool_use block"),
        }
    }
}
