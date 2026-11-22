//! The `sargo` binary: start-up only. Everything else is in the library (`lib.rs`).

use sargo::config::Config;
use sargo::http_server::{HttpServer, StdHttpServer};
use sargo::providers::{AnthropicProvider, OpenAiProvider};
use sargo::gcnii::users::UserModels;
use sargo::router::Router;
use std::sync::Arc;

fn main() {
    // `sargo gcnii train|eval|infer ...`: the GCNII* command line, no server
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.first().map(String::as_str) == Some("gcnii") {
        std::process::exit(sargo::gcnii::cli::run(&args[1..]));
    }

    // The config is loaded once and then only borrowed (`&config`, `&config.server`, ...).
    let config = match Config::load() {
        Ok(config) => config.with_env_overrides(),
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

    // the optional GCNII* model of `[gcnii] weights`
    let gcnii = match config.gcnii.weights.as_str() {
        "" => None,
        path => match sargo::gcnii::weights::load(std::path::Path::new(path)) {
            Ok(model) => Some(model),
            Err(message) => {
                eprintln!("{}", message);
                std::process::exit(1);
            }
        },
    };

    // the optional per-user models of `[gcnii] models_dir`
    let users = match config.gcnii.models_dir.as_str() {
        "" => None,
        dir => match UserModels::new(std::path::Path::new(dir)) {
            Ok(users) => Some(Arc::new(users)),
            Err(message) => {
                eprintln!("{}", message);
                std::process::exit(1);
            }
        },
    };

    if anthropic.is_none() && openai.is_none() && gcnii.is_none() && users.is_none() {
        eprintln!(
            "no provider enabled: set {} and/or {} (or [gcnii] weights / models_dir)",
            config.providers.anthropic.api_key_env, config.providers.openai.api_key_env
        );
        std::process::exit(1);
    }

    // `router` owns the providers. The server only borrows it (`&router`) and runs until killed;
    // `router` lives until the end of `main`, so the borrow is always valid.
    let router = Router::new(anthropic, openai);
    let router = match gcnii {
        Some(model) => router.with_gcnii(model, config.gcnii.classes.clone()),
        None => router,
    };
    let router = match users {
        Some(users) => router.with_gcnii_users(users),
        None => router,
    };
    let server = StdHttpServer::new(&config.server);
    if let Err(message) = server.serve(&router) {
        eprintln!("{}", message);
        std::process::exit(1);
    }
}
