//! The model request router: receives an `InferenceRequest`, forwards it to the provider it
//! names, and returns the provider's answer. It keeps no state between requests (tool calling
//! works because the caller sends the whole conversation each time, see `types.rs`).
//!
//! Endpoints:
//!   POST /v1/infer   body: InferenceRequest JSON  ->  InferenceResponse JSON
//!   POST /v1/gcnii/infer   body: {"graph": {...}, "user_id"?, "classes"?} -> GCNII* class + probabilities
//!   POST /v1/gcnii/embed   body: {"graph": {...}, "user_id"?, "normalize"?} -> the graph embedding
//!       (both use the model of `user_id` (see below) when it is given, else the global `[gcnii] weights`)
//!   POST /v1/gcnii/link    body: {"graph": {...}, "node": {"name"?, "type"?, "neighbors"?: [idx..]}, "user_id"?, "top_k"?, "method"?} -> link prediction:
//!       the score (0..1) of a new node against every node of the graph, best first; `method` is `cosine` (of the node embeddings) or
//!       `head` (the trained link head), by default the head if the model has one
//!   POST /v1/gcnii/link/eval  body: {"graphs": [{n, edges, nodes?}, ...], "user_id"?, "nodes"?, "reveal"?, "seed"?} -> both methods tested on nodes
//!       held out of these graphs: {"cases", "cosine": {auc, mrr, hits_at_10}, "head": {...} | null} (see `gcnii/link_eval.rs`)
//!   POST /v1/gcnii/train   body: {"user_id", "graphs": [{n, edges, nodes?, class}, ...], validation?: [same], epochs?, lr?, batch?, hidden?, depth?, seed?,
//!       task?: "classify" | "link" (link: also holdout?, encoder?: "frozen" | "train", class_weight?, init?; `class` is optional, see `gcnii/link_train.rs`)}
//!       -> 202 {"user_id", "job"}: starts training that user's model in the background (needs `[gcnii] models_dir`);
//!       409 if that user is already training
//!   POST /v1/gcnii/train/status   body: {"user_id"} -> {"user_id", "trained", "job"} (job: running | done | failed | cancelled)
//!   POST /v1/gcnii/train/cancel   body: {"user_id"} -> 202 {"user_id", "trained", "job"}: stops the training of that user (it ends as
//!       `cancelled` at its next batch); 409 if that user is not training. A running job also reports `epoch` and `epoch_loss`; any job reports `epoch_losses` (the mean loss of every finished epoch).
//!   GET  /health     ->  {"status":"ok"}
//! Errors are `{"error": "..."}` with status 400 (bad request), 502 (provider failed),
//! 503 (provider not configured).
//!
//! MEMORY NOTE: the `Router` owns the providers (and so the API keys). The HTTP server shares
//! it between threads as `&Router`; since nothing in it changes after start-up, no lock is needed.
//! A request body is borrowed as `&[u8]`, parsed into an owned `InferenceRequest`, then lent
//! (`&`) to the provider. Nothing is cloned on the way.

use crate::error::RouterError;
use crate::gcnii::embed::embed as gcnii_embed;
use crate::gcnii::graph::Graph;
use crate::gcnii::link::{predict as gcnii_link, Method, NewNode};
use crate::gcnii::link_eval::{compare as gcnii_link_compare, EvalOptions};
use crate::gcnii::model::GcniiStar;
use crate::gcnii::train::infer as gcnii_infer;
use crate::gcnii::users::{link_options_from_value, options_from_value, UserModels};
use crate::http_server::{HttpRequest, HttpResponse, RequestHandler};
use crate::providers::{AnthropicProvider, OpenAiProvider, Provider};
use crate::types::{InferenceRequest, InferenceResponse};
use serde_json::{json, Value};
use std::sync::Arc;

pub struct Router {
    /// `None` = no API key was configured for that provider.
    anthropic: Option<AnthropicProvider>,
    openai: Option<OpenAiProvider>,
    /// A loaded GCNII* model and its class names; `None` = `POST /v1/gcnii/infer` is disabled.
    gcnii: Option<(GcniiStar, Vec<String>)>,
    /// The per-user GCNII* models; `None` = `user_id` requests and training are disabled (no `[gcnii] models_dir`).
    users: Option<Arc<UserModels>>,
}

impl Router {
    /// Takes ownership of the providers (moved in).
    pub fn new(anthropic: Option<AnthropicProvider>, openai: Option<OpenAiProvider>) -> Router {
        Router {
            anthropic,
            openai,
            gcnii: None,
            users: None,
        }
    }

    /// The same router with per-user GCNII* models (training and inference by `user_id`).
    pub fn with_gcnii_users(self, users: Arc<UserModels>) -> Router {
        Router {
            users: Some(users),
            ..self
        }
    }

    /// The same router with a GCNII* model served at `POST /v1/gcnii/infer`.
    pub fn with_gcnii(self, model: GcniiStar, classes: Vec<String>) -> Router {
        Router {
            gcnii: Some((model, classes)),
            ..self
        }
    }

    /// The model for a request: the one of its `user_id` when given (404 if that user has none), else the global one.
    /// Returns the model and the class names it has by default.
    fn gcnii_model(&self, doc: &Value) -> Result<ModelRef<'_>, RouterError> {
        match doc.get("user_id").and_then(|v| v.as_str()) {
            Some(user_id) => {
                let users = self.users_or_503()?;
                let found = users.get(user_id).map_err(user_error)?;
                found
                    .map(ModelRef::User)
                    .ok_or_else(|| RouterError::new(404, &format!("user {user_id} has no trained gcnii model")))
            }
            None => self
                .gcnii
                .as_ref()
                .map(|(model, classes)| ModelRef::Global(model, classes))
                .ok_or_else(|| RouterError::new(503, "gcnii is not configured (set [gcnii] weights, or pass a user_id)")),
        }
    }

    fn users_or_503(&self) -> Result<&Arc<UserModels>, RouterError> {
        self.users
            .as_ref()
            .ok_or_else(|| RouterError::new(503, "per-user gcnii is not configured (set [gcnii] models_dir)"))
    }

    /// `POST /v1/gcnii/embed`: body `{"graph": {...}, "user_id"?, "normalize": bool?}` -> `{"embedding": [..], "dim": N}`.
    fn gcnii_embed(&self, body: &[u8]) -> Result<Value, RouterError> {
        let doc = parse_body(body)?;
        let model = self.gcnii_model(&doc)?;
        let graph = Graph::from_value(doc.get("graph").unwrap_or(&doc))
            .map_err(|err| RouterError::new(400, &format!("invalid graph: {}", err)))?;
        let normalize = doc.get("normalize").and_then(|v| v.as_bool()).unwrap_or(false);
        let embedding = gcnii_embed(model.model(), &graph, normalize);
        Ok(json!({"embedding": embedding, "dim": embedding.len()}))
    }

    /// `POST /v1/gcnii/link`: body `{"graph": {...}, "node": {...}, "user_id"?, "top_k"?}` -> the scores of the new node
    /// against every node of the graph (see `gcnii/link.rs`).
    fn gcnii_link(&self, body: &[u8]) -> Result<Value, RouterError> {
        let doc = parse_body(body)?;
        let model = self.gcnii_model(&doc)?;
        let graph = Graph::from_value(doc.get("graph").ok_or_else(|| RouterError::new(400, "missing graph"))?)
            .map_err(|err| RouterError::new(400, &format!("invalid graph: {}", err)))?;
        let node = match doc.get("node") {
            Some(v) => NewNode::from_value(v).map_err(|err| RouterError::new(400, &format!("invalid node: {}", err)))?,
            None => NewNode::default(),
        };
        let top_k = match doc.get("top_k") {
            None | Some(Value::Null) => None,
            Some(v) => Some(
                v.as_u64()
                    .filter(|&k| k >= 1)
                    .ok_or_else(|| RouterError::new(400, "top_k must be a whole number >= 1"))? as usize,
            ),
        };
        let method = match doc.get("method") {
            None | Some(Value::Null) => None,
            Some(v) => Some(Method::parse(v.as_str().unwrap_or("")).map_err(|err| RouterError::new(400, &err))?),
        };
        gcnii_link(model.model(), &graph, &node, top_k, method).map_err(|err| RouterError::new(400, &err))
    }

    /// `POST /v1/gcnii/link/eval`: body `{"graphs": [...], "user_id"?, "nodes"?, "reveal"?, "seed"?}` -> both ways to predict links tested on the
    /// same held-out nodes of the graphs (see `gcnii/link_eval.rs`); `head` is null when the model has no link head.
    fn gcnii_link_eval(&self, body: &[u8]) -> Result<Value, RouterError> {
        let doc = parse_body(body)?;
        let model = self.gcnii_model(&doc)?;
        let graphs = doc
            .get("graphs")
            .and_then(|g| g.as_array())
            .ok_or_else(|| RouterError::new(400, "graphs must be an array of graph documents"))?;
        let defaults = EvalOptions::default();
        let opts = EvalOptions {
            nodes: doc.get("nodes").map_or(Some(defaults.nodes as u64), |v| v.as_u64().filter(|&n| n >= 1)).ok_or_else(|| RouterError::new(400, "nodes must be a whole number >= 1"))? as usize,
            reveal: doc.get("reveal").map_or(Some(defaults.reveal as f64), |v| v.as_f64().filter(|&r| r > 0.0 && r < 1.0)).ok_or_else(|| RouterError::new(400, "reveal must be a number between 0 and 1"))? as f32,
            seed: doc.get("seed").map_or(Some(defaults.seed), |v| v.as_u64()).ok_or_else(|| RouterError::new(400, "seed must be a whole number"))?,
        };
        let samples = graphs
            .iter()
            .enumerate()
            .map(|(i, g)| {
                Graph::from_value(g)
                    .map(|graph| crate::gcnii::dataset::Sample { path: format!("graphs[{i}]"), graph, label: 0 })
                    .map_err(|err| RouterError::new(400, &format!("graphs[{i}]: {err}")))
            })
            .collect::<Result<Vec<_>, _>>()?;
        gcnii_link_compare(model.model(), &samples, &opts).map_err(|err| RouterError::new(400, &err))
    }

    /// `POST /v1/gcnii/infer`: body `{"graph": {n, edges, nodes?}, "user_id"?, "classes": [..]?}` (a bare graph document
    /// is accepted too); `classes` overrides the class names of the model.
    fn gcnii_infer(&self, body: &[u8]) -> Result<Value, RouterError> {
        let doc = parse_body(body)?;
        let model = self.gcnii_model(&doc)?;
        let graph = Graph::from_value(doc.get("graph").unwrap_or(&doc))
            .map_err(|err| RouterError::new(400, &format!("invalid graph: {}", err)))?;
        let classes: Vec<String> = match doc.get("classes").and_then(|c| c.as_array()) {
            Some(names) => names
                .iter()
                .filter_map(|c| c.as_str().map(String::from))
                .collect(),
            None => model.classes().to_vec(),
        };
        Ok(gcnii_infer(model.model(), &graph, &classes))
    }

    /// `POST /v1/gcnii/train`: starts the training of the model of `user_id` (see the module docs). 202 when started.
    fn gcnii_train(&self, body: &[u8]) -> Result<Value, RouterError> {
        let doc = parse_body(body)?;
        let users = self.users_or_503()?;
        let user_id = doc.get("user_id").and_then(|v| v.as_str()).unwrap_or("");
        let graphs = doc
            .get("graphs")
            .and_then(|g| g.as_array())
            .ok_or_else(|| RouterError::new(400, "graphs must be an array of graph documents"))?;
        let validation = match doc.get("validation").filter(|v| !v.is_null()) {
            None => &[][..],
            Some(v) => v
                .as_array()
                .ok_or_else(|| RouterError::new(400, "validation must be an array of graph documents"))?,
        };
        match doc.get("task").and_then(|t| t.as_str()).unwrap_or("classify") {
            "classify" => {
                let opts = options_from_value(&doc).map_err(user_error)?;
                users.start(user_id, graphs, validation, opts).map_err(user_error)
            }
            "link" => {
                let (opts, from_current) = link_options_from_value(&doc).map_err(user_error)?;
                users.start_link(user_id, graphs, validation, opts, from_current).map_err(user_error)
            }
            other => Err(RouterError::new(400, &format!("unknown task {other:?} (classify or link)"))),
        }
    }

    /// `POST /v1/gcnii/train/status`: body `{"user_id"}`.
    fn gcnii_train_status(&self, body: &[u8]) -> Result<Value, RouterError> {
        let doc = parse_body(body)?;
        let users = self.users_or_503()?;
        let user_id = doc.get("user_id").and_then(|v| v.as_str()).unwrap_or("");
        users.status(user_id).map_err(user_error)
    }

    /// `POST /v1/gcnii/train/cancel`: body `{"user_id"}`; stops the training of that user (409 if none runs).
    fn gcnii_train_cancel(&self, body: &[u8]) -> Result<Value, RouterError> {
        let doc = parse_body(body)?;
        let users = self.users_or_503()?;
        let user_id = doc.get("user_id").and_then(|v| v.as_str()).unwrap_or("");
        users.cancel(user_id).map_err(user_error)
    }

    /// Finds the provider called `name`. The result BORROWS from `self`: it is a reference to a
    /// provider stored inside the router, valid as long as the router is.
    /// To add a provider: add a field above and one arm here.
    fn pick_provider(&self, name: &str) -> Result<&dyn Provider, RouterError> {
        match name {
            "anthropic" => match &self.anthropic {
                Some(provider) => Ok(provider as &dyn Provider),
                None => Err(RouterError::new(503, "anthropic is not configured (no API key)")),
            },
            "openai" => match &self.openai {
                Some(provider) => Ok(provider as &dyn Provider),
                None => Err(RouterError::new(503, "openai is not configured (no API key)")),
            },
            _ => Err(RouterError::new(
                400,
                &format!("unknown provider '{}', use 'anthropic' or 'openai'", name),
            )),
        }
    }

    /// The whole job: parse, pick provider, forward, return the reply.
    fn infer(&self, body: &[u8]) -> Result<InferenceResponse, RouterError> {
        let request: InferenceRequest = serde_json::from_slice(body)
            .map_err(|err| RouterError::new(400, &format!("invalid request JSON: {}", err)))?;
        let provider = self.pick_provider(&request.provider)?;
        provider.infer(&request)
    }
}

impl RequestHandler for Router {
    fn handle(&self, request: &HttpRequest) -> HttpResponse {
        match (request.method.as_str(), request.path.as_str()) {
            ("GET", "/health") => HttpResponse::json(200, json!({"status": "ok"}).to_string()),
            ("POST", "/v1/infer") => match self.infer(&request.body) {
                Ok(answer) => match serde_json::to_string(&answer) {
                    Ok(text) => HttpResponse::json(200, text),
                    Err(err) => error_response(&RouterError::new(
                        500,
                        &format!("cannot encode reply: {}", err),
                    )),
                },
                Err(err) => error_response(&err),
            },
            ("POST", "/v1/gcnii/infer") => match self.gcnii_infer(&request.body) {
                Ok(answer) => HttpResponse::json(200, answer.to_string()),
                Err(err) => error_response(&err),
            },
            ("POST", "/v1/gcnii/embed") => match self.gcnii_embed(&request.body) {
                Ok(answer) => HttpResponse::json(200, answer.to_string()),
                Err(err) => error_response(&err),
            },
            ("POST", "/v1/gcnii/link") => match self.gcnii_link(&request.body) {
                Ok(answer) => HttpResponse::json(200, answer.to_string()),
                Err(err) => error_response(&err),
            },
            ("POST", "/v1/gcnii/link/eval") => match self.gcnii_link_eval(&request.body) {
                Ok(answer) => HttpResponse::json(200, answer.to_string()),
                Err(err) => error_response(&err),
            },
            ("POST", "/v1/gcnii/train") => match self.gcnii_train(&request.body) {
                Ok(answer) => HttpResponse::json(202, answer.to_string()),
                Err(err) => error_response(&err),
            },
            ("POST", "/v1/gcnii/train/status") => match self.gcnii_train_status(&request.body) {
                Ok(answer) => HttpResponse::json(200, answer.to_string()),
                Err(err) => error_response(&err),
            },
            ("POST", "/v1/gcnii/train/cancel") => match self.gcnii_train_cancel(&request.body) {
                Ok(answer) => HttpResponse::json(202, answer.to_string()),
                Err(err) => error_response(&err),
            },
            _ => error_response(&RouterError::new(404, "not found")),
        }
    }
}

fn error_response(err: &RouterError) -> HttpResponse {
    HttpResponse::json(err.status, json!({"error": err.message}).to_string())
}

fn parse_body(body: &[u8]) -> Result<Value, RouterError> {
    serde_json::from_slice(body).map_err(|err| RouterError::new(400, &format!("invalid request JSON: {}", err)))
}

fn user_error((status, message): (u16, String)) -> RouterError {
    RouterError::new(status, &message)
}

/// The model a gcnii request runs on: a user's (shared, not copied) or the global one (borrowed from the router).
enum ModelRef<'a> {
    User(Arc<crate::gcnii::users::UserModel>),
    Global(&'a GcniiStar, &'a [String]),
}

impl ModelRef<'_> {
    fn model(&self) -> &GcniiStar {
        match self {
            ModelRef::User(m) => &m.model,
            ModelRef::Global(model, _) => model,
        }
    }

    fn classes(&self) -> &[String] {
        match self {
            ModelRef::User(m) => &m.classes,
            ModelRef::Global(_, classes) => classes,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn post(router: &Router, path: &str, body: Value) -> (u16, Value) {
        let response = router.handle(&HttpRequest {
            method: "POST".to_string(),
            path: path.to_string(),
            headers: Vec::new(),
            body: body.to_string().into_bytes(),
        });
        (response.status, serde_json::from_slice(&response.body).unwrap())
    }

    #[test]
    fn trains_and_infers_per_user_over_the_router() {
        let dir = std::env::temp_dir().join(format!("sargo_router_gcnii_{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let router = Router::new(None, None).with_gcnii_users(Arc::new(UserModels::new(&dir).unwrap()));
        let graph = json!({"n": 3, "edges": [[0, 1], [1, 2]]});
        let graphs: Vec<Value> = (0..4)
            .flat_map(|_| {
                vec![
                    json!({"n": 3, "edges": [[0, 1], [1, 2]], "class": "path"}),
                    json!({"n": 3, "edges": [[0, 1], [1, 2], [0, 2]], "class": "tri"}),
                ]
            })
            .collect();
        assert_eq!(post(&router, "/v1/gcnii/infer", json!({"user_id": "u1", "graph": graph})).0, 404);
        assert_eq!(post(&router, "/v1/gcnii/train", json!({"graphs": graphs})).0, 400, "user_id is required");
        let body = json!({"user_id": "u1", "graphs": graphs, "epochs": 3, "hidden": 4, "depth": 2});
        assert_eq!(post(&router, "/v1/gcnii/train", body).0, 202);
        let done = (0..600)
            .find_map(|_| {
                std::thread::sleep(std::time::Duration::from_millis(50));
                let (_, status) = post(&router, "/v1/gcnii/train/status", json!({"user_id": "u1"}));
                (status["job"]["status"] != "running").then_some(status)
            })
            .expect("training finished");
        assert_eq!(done["job"]["status"], "done", "{done}");
        let (status, out) = post(&router, "/v1/gcnii/infer", json!({"user_id": "u1", "graph": graph}));
        assert_eq!(status, 200);
        assert!(out["probabilities"]["path"].is_number(), "{out}");
        assert_eq!(post(&router, "/v1/gcnii/infer", json!({"user_id": "u2", "graph": graph})).0, 404);
        assert_eq!(post(&router, "/v1/gcnii/embed", json!({"user_id": "u1", "graph": graph})).0, 200);

        // link prediction, both ways: the cosine of the classifier, then the head trained on top of it
        let cluster = json!({"n": 5, "edges": [[0, 1], [1, 2], [2, 3], [3, 4], [0, 2], [1, 3]]});
        let link = |extra: Value| {
            let mut body = json!({"user_id": "u1", "graph": cluster, "node": {"neighbors": [1]}});
            body.as_object_mut().unwrap().extend(extra.as_object().unwrap().clone());
            post(&router, "/v1/gcnii/link", body)
        };
        assert_eq!(link(json!({})).1["method"], "cosine", "no head yet");
        assert_eq!(link(json!({"method": "head"})).0, 400);
        assert_eq!(link(json!({"method": "nope"})).0, 400);
        let eval = |user: &str| post(&router, "/v1/gcnii/link/eval", json!({"user_id": user, "graphs": [cluster, cluster]}));
        let (status, out) = eval("u1");
        assert_eq!(status, 200, "{out}");
        assert!(out["cosine"]["auc"].is_number() && out["head"].is_null(), "{out}");
        assert_eq!(eval("u2").0, 404);
        assert_eq!(post(&router, "/v1/gcnii/train", json!({"user_id": "u1", "task": "x", "graphs": cluster})).0, 400);
        let linked: Vec<Value> = (0..4).map(|_| cluster.clone()).collect();
        let body = json!({"user_id": "u1", "task": "link", "graphs": linked, "epochs": 3, "seed": 1});
        assert_eq!(post(&router, "/v1/gcnii/train", body).0, 202);
        let done = (0..600)
            .find_map(|_| {
                std::thread::sleep(std::time::Duration::from_millis(50));
                let (_, status) = post(&router, "/v1/gcnii/train/status", json!({"user_id": "u1"}));
                (status["job"]["status"] != "running").then_some(status)
            })
            .expect("link training finished");
        assert_eq!(done["job"]["status"], "done", "{done}");
        assert_eq!(link(json!({})).1["method"], "head");
        assert_eq!(link(json!({"method": "cosine"})).1["method"], "cosine");
        assert!(eval("u1").1["head"]["auc"].is_number());
        let (status, out) = post(&router, "/v1/gcnii/infer", json!({"user_id": "u1", "graph": graph}));
        assert_eq!(status, 200, "the classifier still works: {out}");
        let _ = std::fs::remove_dir_all(dir);
    }

    #[test]
    fn per_user_endpoints_are_503_without_models_dir() {
        let router = Router::new(None, None);
        assert_eq!(post(&router, "/v1/gcnii/train", json!({"user_id": "u", "graphs": []})).0, 503);
        assert_eq!(post(&router, "/v1/gcnii/train/status", json!({"user_id": "u"})).0, 503);
    }
}
