//! The ONLY place that makes outgoing HTTP calls (to the model providers).
//! To swap the HTTP client library later, change just this file.

use crate::error::RouterError;
use serde_json::Value;
use std::time::Duration;

/// POSTs `body` as JSON to `url` with the extra `headers` (name, value pairs) and returns the
/// JSON answer.
///
/// MEMORY NOTE: everything is passed by reference (`&str`, `&[...]`, `&Value`); this function
/// only reads them. The returned `Value` is newly created and owned by the caller.
///
/// This is blocking: the calling thread waits until the provider answers (models can take a
/// while, hence the long timeout). The server runs one thread per connection, so that is fine.
pub fn post_json(url: &str, headers: &[(&str, &str)], body: &Value) -> Result<Value, RouterError> {
    // `ureq` builder methods take `self` and return the changed request, hence the re-assignment.
    let mut request = ureq::post(url)
        .set("content-type", "application/json")
        .timeout(Duration::from_secs(300));
    for &(name, value) in headers {
        request = request.set(name, value);
    }

    match request.send_string(&body.to_string()) {
        Ok(response) => {
            // `into_string` consumes the response (moves it) and gives the body text.
            let text = response
                .into_string()
                .map_err(|err| RouterError::new(502, &format!("cannot read provider reply: {}", err)))?;
            serde_json::from_str(&text)
                .map_err(|err| RouterError::new(502, &format!("provider reply is not JSON: {}", err)))
        }
        // The provider answered with an HTTP error (4xx/5xx). Pass its text on, it usually says why.
        Err(ureq::Error::Status(code, response)) => {
            let text = response.into_string().unwrap_or_default();
            Err(RouterError::new(
                502,
                &format!("provider returned HTTP {}: {}", code, text),
            ))
        }
        // Could not talk to the provider at all (DNS, connection, TLS, timeout...).
        Err(err) => Err(RouterError::new(
            502,
            &format!("cannot reach provider: {}", err),
        )),
    }
}
