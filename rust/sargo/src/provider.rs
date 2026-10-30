//! The interface every model provider implements. The router only knows this trait,
//! so adding a provider = a new file with a struct that implements `Provider`, plus one
//! line in `router.rs` to pick it.

use crate::error::RouterError;
use crate::types::{InferenceRequest, InferenceResponse};

pub trait Provider {
    /// Short name used in requests, e.g. "anthropic".
    fn name(&self) -> &str;

    /// Sends the request to the provider and returns its answer in sargo's neutral format.
    /// The request is only borrowed: the provider reads it and builds its own JSON from it.
    fn infer(&self, request: &InferenceRequest) -> Result<InferenceResponse, RouterError>;
}
