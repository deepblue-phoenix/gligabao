//! The structs sargo's callers use. These are provider-neutral: the caller never writes
//! Anthropic or OpenAI JSON, it writes this, and each provider module translates it.
//!
//! The shape follows Anthropic's "content blocks" because that model can express everything
//! OpenAI can (text, tool calls, tool results), so translating to OpenAI loses nothing.
//!
//! ## Tool calling flow (sargo itself is stateless, the CALLER runs the loop)
//!
//! 1. Caller POSTs an `InferenceRequest` with `tools` set.
//! 2. Model wants a tool: the reply has `stop_reason == "tool_use"` and `content` holds one or
//!    more `ToolUse` blocks (`id`, `name`, `input`).
//! 3. Caller runs the tool(s) itself, then POSTs the SAME request again with two messages added at
//!    the end of `messages`:
//!      - an `assistant` message containing the `content` blocks it just received, unchanged;
//!      - a `user` message containing one `ToolResult` block per `ToolUse` (same id in `tool_use_id`).
//! 4. Repeat until `stop_reason == "end_turn"`.
//!
//! MEMORY NOTE: all these structs OWN their data (`String`, `Vec`). A request is parsed from the
//! HTTP body once, then only passed around by reference (`&InferenceRequest`), never copied.

use serde::{Deserialize, Serialize};
use serde_json::Value;

/// A request to run one model inference. Arrives as JSON on `POST /v1/infer`.
#[derive(Debug, Deserialize)]
pub struct InferenceRequest {
    /// Which provider to forward to: "anthropic" or "openai".
    pub provider: String,
    /// The provider's own model name, passed through untouched (e.g. "claude-sonnet-4-5", "gpt-4o").
    pub model: String,
    /// Optional system prompt.
    pub system: Option<String>,
    /// The conversation so far, oldest first.
    pub messages: Vec<Message>,
    /// Tools the model may call. Missing in the JSON = no tools.
    #[serde(default)]
    pub tools: Vec<Tool>,
    /// Max tokens to generate. Missing = `DEFAULT_MAX_TOKENS`.
    pub max_tokens: Option<u32>,
    /// Sampling temperature. Missing = the provider's default.
    pub temperature: Option<f64>,
}

/// Used when the caller does not set `max_tokens` (Anthropic makes the field mandatory).
pub const DEFAULT_MAX_TOKENS: u32 = 1024;

/// One turn of the conversation.
///
/// The derive `Serialize` is needed because the Anthropic provider sends messages as they are
/// (our JSON shape is identical to Anthropic's on purpose).
#[derive(Debug, Deserialize, Serialize)]
pub struct Message {
    /// "user" or "assistant". Tool results are sent in a "user" message (see module docs).
    pub role: String,
    pub content: Vec<ContentBlock>,
}

/// A piece of a message. In JSON it has a `"type"` field: "text", "tool_use" or "tool_result".
#[derive(Debug, Deserialize, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ContentBlock {
    /// Plain text.
    Text { text: String },
    /// The model asks the caller to run a tool. `input` is the tool's arguments as free-form JSON.
    ToolUse {
        id: String,
        name: String,
        input: Value,
    },
    /// The caller's answer to a `ToolUse`. `tool_use_id` must equal the `id` of that `ToolUse`.
    ToolResult {
        tool_use_id: String,
        content: String,
        /// True if the tool failed and `content` is the error text.
        #[serde(default)]
        is_error: bool,
    },
}

/// A tool the model may call.
#[derive(Debug, Deserialize)]
pub struct Tool {
    pub name: String,
    #[serde(default)]
    pub description: String,
    /// JSON Schema describing the tool's arguments (an object schema).
    pub input_schema: Value,
}

/// What sargo answers with. Serialized to JSON as the HTTP reply body.
#[derive(Debug, Serialize)]
pub struct InferenceResponse {
    /// Echo of the provider that answered ("anthropic" or "openai").
    pub provider: String,
    /// The model name the provider reports.
    pub model: String,
    /// The model's answer: `Text` blocks and/or `ToolUse` blocks.
    pub content: Vec<ContentBlock>,
    /// Why the model stopped, normalised across providers:
    /// "end_turn" (finished), "tool_use" (wants tools run), "max_tokens" (cut off),
    /// anything else is the provider's raw value.
    pub stop_reason: String,
    pub usage: Usage,
}

#[derive(Debug, Serialize)]
pub struct Usage {
    pub input_tokens: u32,
    pub output_tokens: u32,
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// `inference.schema.json` is the contract for clients; keep it in step with these structs.
    fn schema() -> Value {
        serde_json::from_str(include_str!("../inference.schema.json")).expect("schema is valid JSON")
    }

    fn property_names(schema: &Value, def: &str) -> Vec<String> {
        let mut names: Vec<String> = schema["$defs"][def]["properties"]
            .as_object()
            .unwrap_or_else(|| panic!("{} has no properties", def))
            .keys()
            .cloned()
            .collect();
        names.sort();
        names
    }

    fn keys(value: &Value) -> Vec<String> {
        let mut names: Vec<String> = value.as_object().unwrap().keys().cloned().collect();
        names.sort();
        names
    }

    #[test]
    fn schema_matches_response_shape() {
        let schema = schema();
        let response = InferenceResponse {
            provider: "anthropic".into(),
            model: "m".into(),
            content: vec![
                ContentBlock::Text { text: "t".into() },
                ContentBlock::ToolUse { id: "1".into(), name: "n".into(), input: json!({}) },
            ],
            stop_reason: "tool_use".into(),
            usage: Usage { input_tokens: 1, output_tokens: 2 },
        };
        let value = serde_json::to_value(&response).unwrap();
        assert_eq!(keys(&value), property_names(&schema, "InferenceResponse"));
        assert_eq!(keys(&value["usage"]), property_names(&schema, "Usage"));
        assert_eq!(keys(&value["content"][0]), property_names(&schema, "ContentBlockText"));
        assert_eq!(keys(&value["content"][1]), property_names(&schema, "ContentBlockToolUse"));
    }

    #[test]
    fn schema_matches_request_shape() {
        let schema = schema();
        let tool_result = serde_json::to_value(ContentBlock::ToolResult {
            tool_use_id: "1".into(),
            content: "c".into(),
            is_error: false,
        })
        .unwrap();
        assert_eq!(keys(&tool_result), property_names(&schema, "ContentBlockToolResult"));
        assert_eq!(
            property_names(&schema, "InferenceRequest"),
            ["max_tokens", "messages", "model", "provider", "system", "temperature", "tools"]
        );
        assert_eq!(property_names(&schema, "Message"), ["content", "role"]);
        assert_eq!(property_names(&schema, "Tool"), ["description", "input_schema", "name"]);
        assert_eq!(schema["$defs"]["InferenceRequest"]["properties"]["max_tokens"]["default"], DEFAULT_MAX_TOKENS);
    }
}
