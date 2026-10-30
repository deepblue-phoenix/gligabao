//! sargo: a small model request router. See `router.rs` for the endpoints and README.md for usage.
//!
//! The package is a library (this file, so that other crates can build on it) and the `sargo` binary (`main.rs`).
//!
//! Layout:
//!   main.rs         the binary: load the config, build the router, start the server
//!   config.rs       config.toml -> `Config` struct, passed to the parts that need it
//!   http_server.rs  HTTP server (std only) behind the `HttpServer` trait  <- swap the server here
//!   http_client.rs  outgoing HTTP calls to the providers                  <- swap the client here
//!   router.rs       request -> provider -> reply
//!   providers/      the providers
//!     mod.rs          the `Provider` trait
//!     anthropic.rs, openai.rs   one provider each (format translation, tool calling)
//!   types.rs        the neutral request/response structs
//!   json_util.rs, error.rs     small helpers

pub mod config;
pub mod error;
pub mod http_client;
pub mod http_server;
pub mod json_util;
pub mod providers;
pub mod router;
pub mod types;
