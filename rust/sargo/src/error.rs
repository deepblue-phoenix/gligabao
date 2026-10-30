//! One small error type used everywhere in sargo.
//!
//! MEMORY NOTE: `RouterError` owns its `message` (a `String`). Errors are created rarely and
//! are tiny, so we simply build an owned String instead of borrowing from something.

#[derive(Debug)]
pub struct RouterError {
    /// HTTP status that sargo answers its own caller with:
    /// 400 = the caller's request is bad, 502 = the model provider failed or answered garbage,
    /// 503 = the provider is not configured (no API key).
    pub status: u16,
    pub message: String,
}

impl RouterError {
    /// `message` is borrowed (`&str`); we copy it into an owned String here, so the error can
    /// outlive whatever the message came from.
    pub fn new(status: u16, message: &str) -> RouterError {
        RouterError {
            status,
            message: message.to_string(),
        }
    }
}
