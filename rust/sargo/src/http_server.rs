//! A very small HTTP/1.1 server built on the Rust standard library only.
//!
//! ## How to swap in another server (hyper, axum, tiny_http, ...)
//! The rest of sargo only depends on the three items below: `HttpRequest`, `HttpResponse`,
//! `RequestHandler` (what we implement) and `HttpServer` (what the server library implements).
//! To change library: write a new struct that implements `HttpServer`, converting the library's
//! request type into `HttpRequest` and our `HttpResponse` back; then change one line in `main.rs`.
//! Nothing in `router.rs` needs to change.
//!
//! Limits of this implementation (fine for now): one request per connection (`Connection: close`),
//! no keep-alive, no chunked request bodies, no TLS (put a reverse proxy in front for https).
//!
//! ## Memory / threading model (read this before changing `serve`)
//! * One OS thread per connection, started with `std::thread::scope`.
//! * A *scoped* thread may BORROW local variables, because the scope guarantees every thread
//!   finishes before the scope ends. So all threads share one `&dyn RequestHandler` (a plain
//!   reference), with no `Arc` and no cloning of the router.
//! * That sharing is only allowed because `RequestHandler: Sync` (see the trait): the compiler
//!   checks that the handler is safe to use from several threads at once. This means the handler
//!   can only read its own data (`&self`, not `&mut self`). If you ever need mutable shared state
//!   in the handler, wrap that state in a `std::sync::Mutex`.
//! * Each connection's `TcpStream` is MOVED into its thread (`move` closure) and dropped, which
//!   closes the socket, when the thread ends.

use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::thread;
use std::time::Duration;

/// Refuse bodies larger than this (10 MB) so a bad client cannot make us allocate without limit.
const MAX_BODY_BYTES: usize = 10 * 1024 * 1024;
/// Refuse requests with more header lines than this.
const MAX_HEADERS: usize = 100;

/// A parsed incoming request. Owns its data; the handler receives it by reference.
pub struct HttpRequest {
    /// "GET", "POST", ...
    pub method: String,
    /// The path without the query string, e.g. "/v1/infer".
    pub path: String,
    /// Header (name, value) pairs. Names are lower-cased.
    pub headers: Vec<(String, String)>,
    /// Raw body bytes (empty if none).
    pub body: Vec<u8>,
}

impl HttpRequest {
    /// Looks up a header by (lower-case) name. Returns a *borrow* into `self.headers`:
    /// the `&str` is only valid while this request is alive, which the compiler enforces.
    pub fn header(&self, name: &str) -> Option<&str> {
        for (header_name, value) in &self.headers {
            if header_name == name {
                return Some(value);
            }
        }
        None
    }
}

/// What a handler returns. The server writes it to the socket.
pub struct HttpResponse {
    pub status: u16,
    pub content_type: String,
    pub body: Vec<u8>,
}

impl HttpResponse {
    /// A JSON response. `body` is MOVED in (no copy): the caller can no longer use that String.
    pub fn json(status: u16, body: String) -> HttpResponse {
        HttpResponse {
            status,
            content_type: "application/json".to_string(),
            body: body.into_bytes(), // `into_bytes` reuses the String's memory
        }
    }

    /// A plain text response (used for simple errors).
    pub fn text(status: u16, body: &str) -> HttpResponse {
        HttpResponse {
            status,
            content_type: "text/plain; charset=utf-8".to_string(),
            body: body.as_bytes().to_vec(),
        }
    }
}

/// The application side: turns a request into a response. `router::Router` implements this.
///
/// `Sync` is required because several connection threads call `handle` at the same time on the
/// same object (see the module docs). `&self` (not `&mut self`) for the same reason.
pub trait RequestHandler: Sync {
    fn handle(&self, request: &HttpRequest) -> HttpResponse;
}

/// The server side: listens and calls the handler for every request. Implement this to swap
/// the HTTP server library.
pub trait HttpServer {
    /// Runs forever (only returns an `Err` if it cannot start). The handler is borrowed for the
    /// whole run, so it must outlive the call: the caller keeps ownership.
    fn serve(&self, handler: &dyn RequestHandler) -> Result<(), String>;
}

/// The standard-library implementation of `HttpServer`.
pub struct StdHttpServer {
    /// Address to listen on, like "127.0.0.1:8080".
    address: String,
}

impl StdHttpServer {
    pub fn new(address: &str) -> StdHttpServer {
        StdHttpServer {
            address: address.to_string(),
        }
    }
}

impl HttpServer for StdHttpServer {
    fn serve(&self, handler: &dyn RequestHandler) -> Result<(), String> {
        let listener = TcpListener::bind(&self.address)
            .map_err(|err| format!("cannot listen on {}: {}", self.address, err))?;
        println!("sargo listening on http://{}", self.address);

        // `thread::scope`: threads started with `scope.spawn` may borrow `handler` (see module docs).
        thread::scope(|scope| {
            for incoming in listener.incoming() {
                let stream = match incoming {
                    Ok(stream) => stream,
                    Err(err) => {
                        eprintln!("accept failed: {}", err);
                        continue;
                    }
                };
                // `move`: the closure takes ownership of `stream` (a connection belongs to exactly
                // one thread) and copies `handler` (a shared reference is `Copy`, so this is free).
                scope.spawn(move || {
                    handle_connection(stream, handler);
                });
            }
        });
        Ok(())
    }
}

/// Reads one request from the socket, calls the handler, writes the response, closes the socket.
/// `stream` is owned here, so it is closed when this function returns.
fn handle_connection(stream: TcpStream, handler: &dyn RequestHandler) {
    // Do not wait forever for a client that stops sending.
    let _ = stream.set_read_timeout(Some(Duration::from_secs(30)));

    let response = match read_request(&stream) {
        Ok(request) => handler.handle(&request),
        Err(message) => HttpResponse::text(400, &message),
    };

    if let Err(err) = write_response(&stream, &response) {
        eprintln!("cannot write response: {}", err);
    }
}

/// Parses one HTTP/1.1 request. `stream` is only borrowed: `&TcpStream` implements `Read`
/// and `Write`, so reading and writing can both use a shared reference to the same socket.
fn read_request(stream: &TcpStream) -> Result<HttpRequest, String> {
    // BufReader reads big chunks from the socket and hands them out line by line.
    // IMPORTANT: it may read ahead into the body, so the body below MUST be read through the
    // same `reader` (never directly from `stream`), or those bytes would be lost.
    let mut reader = BufReader::new(stream);

    // 1) Request line: "POST /v1/infer HTTP/1.1"
    let mut line = String::new();
    reader
        .read_line(&mut line)
        .map_err(|err| format!("cannot read request: {}", err))?;
    // `parts` borrows from `line`; `line` must stay alive and unchanged while we use `parts`.
    let mut parts = line.split_whitespace();
    let method = parts.next().ok_or("empty request")?.to_string();
    let target = parts.next().ok_or("missing request path")?;
    // Drop the query string ("/a?x=1" -> "/a"). `path` is copied into an owned String here.
    let path = target.split('?').next().unwrap_or("").to_string();

    // 2) Header lines until an empty line.
    let mut headers: Vec<(String, String)> = Vec::new();
    loop {
        let mut header_line = String::new();
        let read = reader
            .read_line(&mut header_line)
            .map_err(|err| format!("cannot read headers: {}", err))?;
        let trimmed = header_line.trim_end();
        if read == 0 || trimmed.is_empty() {
            break;
        }
        if headers.len() >= MAX_HEADERS {
            return Err("too many headers".to_string());
        }
        if let Some((name, value)) = trimmed.split_once(':') {
            // Copy both into owned Strings because `header_line` is dropped at the end of this loop turn.
            headers.push((name.trim().to_lowercase(), value.trim().to_string()));
        }
    }

    let mut request = HttpRequest {
        method,
        path,
        headers,
        body: Vec::new(),
    };

    // 3) Body, if any. Only "Content-Length" bodies are supported.
    if request.header("transfer-encoding").is_some() {
        return Err("chunked request bodies are not supported".to_string());
    }
    let length: usize = match request.header("content-length") {
        Some(value) => value
            .parse()
            .map_err(|_| "bad content-length".to_string())?,
        None => 0,
    };
    if length > MAX_BODY_BYTES {
        return Err("request body too large".to_string());
    }
    // A zero-filled buffer of exactly the right size, which `read_exact` then fills in place.
    let mut body = vec![0u8; length];
    reader
        .read_exact(&mut body)
        .map_err(|err| format!("cannot read body: {}", err))?;
    request.body = body; // moves `body` into the struct

    Ok(request)
}

/// Writes the response and tells the client we close the connection afterwards.
fn write_response(stream: &TcpStream, response: &HttpResponse) -> std::io::Result<()> {
    let head = format!(
        "HTTP/1.1 {} {}\r\nContent-Type: {}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        response.status,
        reason_phrase(response.status),
        response.content_type,
        response.body.len()
    );
    // `Write` is implemented for `&TcpStream`, and its methods need `&mut self`, so we make a
    // mutable local *reference* (not a copy of the socket).
    let mut writer = stream;
    writer.write_all(head.as_bytes())?;
    writer.write_all(&response.body)?;
    writer.flush()
}

/// The text after the status code in the status line.
fn reason_phrase(status: u16) -> &'static str {
    match status {
        200 => "OK",
        400 => "Bad Request",
        404 => "Not Found",
        500 => "Internal Server Error",
        502 => "Bad Gateway",
        503 => "Service Unavailable",
        _ => "Unknown",
    }
}
