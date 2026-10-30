//! sargo: a small model request router. See `router.rs` for the endpoints and README.md for usage.
//!
//! Layout:
//!   main.rs         start-up: read config, build the router, start the server
//!   http_server.rs  HTTP server (std only) behind the `HttpServer` trait  <- swap the server here
//!   http_client.rs  outgoing HTTP calls to the providers                  <- swap the client here
//!   router.rs       request -> provider -> reply
//!   providers/      the providers
//!     mod.rs          the `Provider` trait
//!     anthropic.rs, openai.rs   one provider each (format translation, tool calling)
//!   types.rs        the neutral request/response structs
//!   json_util.rs, error.rs     small helpers

mod error;
mod http_client;
mod http_server;
mod json_util;
mod providers;
mod router;
mod types;

use http_server::{HttpServer, StdHttpServer};
use providers::{AnthropicProvider, OpenAiProvider};
use router::Router;
use std::env;

/// Reads an environment variable; an unset or empty variable gives `None`.
fn env_var(name: &str) -> Option<String> {
    match env::var(name) {
        Ok(value) if !value.is_empty() => Some(value),
        _ => None,
    }
}

fn main() {
    // Configuration comes from environment variables:
    //   ANTHROPIC_API_KEY, OPENAI_API_KEY   (set at least one)
    //   ANTHROPIC_BASE_URL, OPENAI_BASE_URL (optional)
    //   SARGO_ADDR                          (optional, default 127.0.0.1:8080)
    let address = env_var("SARGO_ADDR").unwrap_or("127.0.0.1:8080".to_string());

    // `map` moves the key into the closure, which builds the provider; `None` stays `None`.
    let anthropic = env_var("ANTHROPIC_API_KEY").map(|key| {
        let base_url = env_var("ANTHROPIC_BASE_URL").unwrap_or("https://api.anthropic.com".to_string());
        AnthropicProvider::new(&key, &base_url)
    });
    let openai = env_var("OPENAI_API_KEY").map(|key| {
        let base_url = env_var("OPENAI_BASE_URL").unwrap_or("https://api.openai.com".to_string());
        OpenAiProvider::new(&key, &base_url)
    });

    if anthropic.is_none() && openai.is_none() {
        eprintln!("set ANTHROPIC_API_KEY and/or OPENAI_API_KEY");
        std::process::exit(1);
    }

    // `router` owns the providers. The server only borrows it (`&router`) and runs until killed;
    // `router` lives until the end of `main`, so the borrow is always valid.
    let router = Router::new(anthropic, openai);
    let server = StdHttpServer::new(&address);
    if let Err(message) = server.serve(&router) {
        eprintln!("{}", message);
        std::process::exit(1);
    }
}
