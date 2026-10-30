//! Configuration: reads `config.toml` into a `Config` struct that is created once in `main` and then
//! passed (by reference) to the parts that need it.
//!
//! Where the file comes from: the first command line argument, else `$SARGO_CONFIG`, else
//! `./config.toml`. A missing file is only an error when the path was given explicitly; with the
//! default path, the built-in defaults are used. Every key is optional (see `config.toml`).
//! Secrets (API keys) are not in the file: the file names the environment variable that holds them.
//!
//! MEMORY NOTE: `Config` owns all its data and is never changed after loading. Parts of sargo
//! borrow what they need (`&config.server`, ...) and copy the few values they keep (small numbers
//! and strings), so they do not depend on the `Config` staying alive.

use serde::Deserialize;
use std::env;
use std::fs;

const DEFAULT_PATH: &str = "config.toml";

#[derive(Debug, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct Config {
    pub server: ServerConfig,
    pub http_client: HttpClientConfig,
    pub providers: ProvidersConfig,
}

#[derive(Debug, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct ServerConfig {
    pub address: String,
    pub read_timeout_secs: u64,
    pub max_body_bytes: usize,
}

#[derive(Debug, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct HttpClientConfig {
    pub timeout_secs: u64,
}

#[derive(Debug, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct ProvidersConfig {
    pub anthropic: ProviderConfig,
    pub openai: ProviderConfig,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ProviderConfig {
    pub base_url: String,
    /// Name of the environment variable holding the API key.
    pub api_key_env: String,
}

impl Default for ServerConfig {
    fn default() -> ServerConfig {
        ServerConfig {
            address: "127.0.0.1:8431".to_string(),
            read_timeout_secs: 30,
            max_body_bytes: 10 * 1024 * 1024,
        }
    }
}

impl Default for HttpClientConfig {
    fn default() -> HttpClientConfig {
        HttpClientConfig { timeout_secs: 300 }
    }
}

impl Default for ProvidersConfig {
    fn default() -> ProvidersConfig {
        ProvidersConfig {
            anthropic: ProviderConfig {
                base_url: "https://api.anthropic.com".to_string(),
                api_key_env: "ANTHROPIC_API_KEY".to_string(),
            },
            openai: ProviderConfig {
                base_url: "https://api.openai.com".to_string(),
                api_key_env: "OPENAI_API_KEY".to_string(),
            },
        }
    }
}

impl ProviderConfig {
    /// The API key from the environment; an unset or empty variable gives `None` (provider disabled).
    pub fn api_key(&self) -> Option<String> {
        match env::var(&self.api_key_env) {
            Ok(value) if !value.is_empty() => Some(value),
            _ => None,
        }
    }
}

impl Config {
    /// Loads the config from the path given on the command line / `$SARGO_CONFIG` / `./config.toml`.
    pub fn load() -> Result<Config, String> {
        let explicit = env::args().nth(1).or_else(|| env::var("SARGO_CONFIG").ok());
        match explicit {
            Some(path) => {
                let text = fs::read_to_string(&path)
                    .map_err(|err| format!("cannot read config {}: {}", path, err))?;
                Config::parse(&text).map_err(|err| format!("{}: {}", path, err))
            }
            None => match fs::read_to_string(DEFAULT_PATH) {
                Ok(text) => Config::parse(&text).map_err(|err| format!("{}: {}", DEFAULT_PATH, err)),
                Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(Config::default()),
                Err(err) => Err(format!("cannot read config {}: {}", DEFAULT_PATH, err)),
            },
        }
    }

    /// Parses config text (TOML). Missing keys get their defaults; unknown keys are an error (typo guard).
    pub fn parse(text: &str) -> Result<Config, String> {
        toml::from_str(text).map_err(|err| err.to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn empty_file_gives_defaults() {
        let config = Config::parse("").unwrap();
        assert_eq!(config.server.address, "127.0.0.1:8431");
        assert_eq!(config.http_client.timeout_secs, 300);
        assert_eq!(config.providers.openai.api_key_env, "OPENAI_API_KEY");
    }

    #[test]
    fn partial_file_overrides_only_what_it_sets() {
        let config = Config::parse("[server]\naddress = \"0.0.0.0:9000\"\n").unwrap();
        assert_eq!(config.server.address, "0.0.0.0:9000");
        assert_eq!(config.server.read_timeout_secs, 30);
    }

    #[test]
    fn unknown_key_is_an_error() {
        assert!(Config::parse("[server]\nadress = \"x\"\n").is_err());
    }

    #[test]
    fn shipped_config_matches_defaults() {
        let shipped = Config::parse(include_str!("../config.toml")).unwrap();
        let defaults = Config::default();
        assert_eq!(format!("{:?}", shipped), format!("{:?}", defaults));
    }
}
