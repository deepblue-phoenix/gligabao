//! sargo: a small model request router. See `router.rs` for the endpoints and README.md for usage.
//!
//! Layout:
//!   main.rs         start-up: load the config, build the router, start the server
//!   config.rs       config.toml -> `Config` struct, passed to the parts that need it
//!   http_server.rs  HTTP server (std only) behind the `HttpServer` trait  <- swap the server here
//!   http_client.rs  outgoing HTTP calls to the providers                  <- swap the client here
//!   router.rs       request -> provider -> reply
//!   providers/      the providers
//!     mod.rs          the `Provider` trait
//!     anthropic.rs, openai.rs   one provider each (format translation, tool calling)
//!   types.rs        the neutral request/response structs
//!   json_util.rs, error.rs     small helpers

mod config;
mod error;
mod http_client;
mod http_server;
mod json_util;
mod providers;
mod router;
mod types;

use config::Config;
use http_server::{HttpServer, StdHttpServer};
use providers::{AnthropicProvider, OpenAiProvider};
use router::Router;

fn main() {
    // The config is loaded once and then only borrowed (`&config`, `&config.server`, ...).
    let config = match Config::load() {
        Ok(config) => config,
        Err(message) => {
            eprintln!("{}", message);
            std::process::exit(1);
        }
    };

    // A provider is enabled when the env var named in its config holds a key. `map` moves the key
    // into the closure, which builds the provider; `None` stays `None`.
    let anthropic = config.providers.anthropic.api_key().map(|key| {
        AnthropicProvider::new(&key, &config.providers.anthropic, &config.http_client)
    });
    let openai = config.providers.openai.api_key().map(|key| {
        OpenAiProvider::new(&key, &config.providers.openai, &config.http_client)
    });

    if anthropic.is_none() && openai.is_none() {
        eprintln!(
            "no provider enabled: set {} and/or {}",
            config.providers.anthropic.api_key_env, config.providers.openai.api_key_env
        );
        std::process::exit(1);
    }

    // `router` owns the providers. The server only borrows it (`&router`) and runs until killed;
    // `router` lives until the end of `main`, so the borrow is always valid.
    let router = Router::new(anthropic, openai);
    let server = StdHttpServer::new(&config.server);
    if let Err(message) = server.serve(&router) {
        eprintln!("{}", message);
        std::process::exit(1);
    }
}
