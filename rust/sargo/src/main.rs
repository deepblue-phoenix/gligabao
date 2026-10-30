//! The `sargo` binary: start-up only. Everything else is in the library (`lib.rs`).

use sargo::config::Config;
use sargo::http_server::{HttpServer, StdHttpServer};
use sargo::providers::{AnthropicProvider, OpenAiProvider};
use sargo::router::Router;

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
