//! The model request router: receives an `InferenceRequest`, forwards it to the provider it
//! names, and returns the provider's answer. It keeps no state between requests (tool calling
//! works because the caller sends the whole conversation each time, see `types.rs`).
//!
//! Endpoints:
//!   POST /v1/infer   body: InferenceRequest JSON  ->  InferenceResponse JSON
//!   GET  /health     ->  {"status":"ok"}
//! Errors are `{"error": "..."}` with status 400 (bad request), 502 (provider failed),
//! 503 (provider not configured).
//!
//! MEMORY NOTE: the `Router` owns the providers (and so the API keys). The HTTP server shares
//! it between threads as `&Router`; since nothing in it changes after start-up, no lock is needed.
//! A request body is borrowed as `&[u8]`, parsed into an owned `InferenceRequest`, then lent
//! (`&`) to the provider. Nothing is cloned on the way.

use crate::anthropic::AnthropicProvider;
use crate::error::RouterError;
use crate::http_server::{HttpRequest, HttpResponse, RequestHandler};
use crate::openai::OpenAiProvider;
use crate::provider::Provider;
use crate::types::{InferenceRequest, InferenceResponse};
use serde_json::json;

pub struct Router {
    /// `None` = no API key was configured for that provider.
    anthropic: Option<AnthropicProvider>,
    openai: Option<OpenAiProvider>,
}

impl Router {
    /// Takes ownership of the providers (moved in).
    pub fn new(anthropic: Option<AnthropicProvider>, openai: Option<OpenAiProvider>) -> Router {
        Router { anthropic, openai }
    }

    /// Finds the provider called `name`. The result BORROWS from `self`: it is a reference to a
    /// provider stored inside the router, valid as long as the router is.
    /// To add a provider: add a field above and one arm here.
    fn pick_provider(&self, name: &str) -> Result<&dyn Provider, RouterError> {
        match name {
            "anthropic" => match &self.anthropic {
                Some(provider) => Ok(provider as &dyn Provider),
                None => Err(RouterError::new(503, "anthropic is not configured (no API key)")),
            },
            "openai" => match &self.openai {
                Some(provider) => Ok(provider as &dyn Provider),
                None => Err(RouterError::new(503, "openai is not configured (no API key)")),
            },
            _ => Err(RouterError::new(
                400,
                &format!("unknown provider '{}', use 'anthropic' or 'openai'", name),
            )),
        }
    }

    /// The whole job: parse, pick provider, forward, return the reply.
    fn infer(&self, body: &[u8]) -> Result<InferenceResponse, RouterError> {
        let request: InferenceRequest = serde_json::from_slice(body)
            .map_err(|err| RouterError::new(400, &format!("invalid request JSON: {}", err)))?;
        let provider = self.pick_provider(&request.provider)?;
        provider.infer(&request)
    }
}

impl RequestHandler for Router {
    fn handle(&self, request: &HttpRequest) -> HttpResponse {
        match (request.method.as_str(), request.path.as_str()) {
            ("GET", "/health") => HttpResponse::json(200, json!({"status": "ok"}).to_string()),
            ("POST", "/v1/infer") => match self.infer(&request.body) {
                Ok(answer) => match serde_json::to_string(&answer) {
                    Ok(text) => HttpResponse::json(200, text),
                    Err(err) => error_response(&RouterError::new(
                        500,
                        &format!("cannot encode reply: {}", err),
                    )),
                },
                Err(err) => error_response(&err),
            },
            _ => error_response(&RouterError::new(404, "not found")),
        }
    }
}

fn error_response(err: &RouterError) -> HttpResponse {
    HttpResponse::json(err.status, json!({"error": err.message}).to_string())
}
