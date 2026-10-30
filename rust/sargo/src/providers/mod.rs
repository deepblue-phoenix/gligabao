//! Model providers: the `Provider` trait and one file per provider. The router only knows the trait,
//! so adding a provider = a new file here with a struct that implements `Provider`, a `mod`/`pub use`
//! line below, plus one line in `router.rs` to pick it.

mod anthropic;
mod openai;

pub use anthropic::AnthropicProvider;
pub use openai::OpenAiProvider;

use crate::error::RouterError;
use crate::types::{InferenceRequest, InferenceResponse};

pub trait Provider {
    /// Short name used in requests, e.g. "anthropic".
    fn name(&self) -> &str;

    /// Sends the request to the provider and returns its answer in sargo's neutral format.
    /// The request is only borrowed: the provider reads it and builds its own JSON from it.
    fn infer(&self, request: &InferenceRequest) -> Result<InferenceResponse, RouterError>;
}
