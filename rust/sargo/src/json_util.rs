//! Tiny helpers to *move* data out of a `serde_json::Value` instead of cloning it.
//!
//! MEMORY NOTE: a `Value` we got from a provider is owned by us. When we convert it into our own
//! structs we want to *take* the pieces (move them), not copy them. `serde_json::Value` indexing
//! (`value["key"]`) only gives a reference, so reading a String out of it means `.to_string()`
//! (a copy). These helpers remove the field from the object and hand over ownership: zero copies.
//!
//! They never panic: a missing key or a wrong type just gives `Value::Null` / an empty String.

use serde_json::Value;

/// Removes `key` from `value` (if `value` is a JSON object) and returns it by value.
/// `value` is borrowed mutably only for the duration of the call; the returned `Value` is owned.
pub fn take_field(value: &mut Value, key: &str) -> Value {
    match value {
        Value::Object(map) => map.remove(key).unwrap_or(Value::Null),
        _ => Value::Null,
    }
}

/// Takes ownership of `value` and returns the String inside it (no copy), or "" if it is not a string.
pub fn take_string(value: Value) -> String {
    match value {
        Value::String(text) => text,
        _ => String::new(),
    }
}
