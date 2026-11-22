//! GCNII*: a graph classifier (the untied variant of GCNII), trained and run on the CPU. Ported from the
//! deepblue engine (baby_thinker); only the GCNII* model type is here, the weights files are compatible.
//!
//!   model.rs     the model: forward, backward, SGD step (immutable, pure functions)
//!   graph.rs     the graph, its normalized adjacency, the JSON graph document
//!   features.rs  graph -> initial matrix X, node type vocabulary
//!   dataset.rs   loading a training / eval dataset
//!   train.rs     `train`, `evaluate`, `infer`
//!   metrics.rs   gradient probes, `--grad-stats`, `--grad-map`
//!   embed.rs     graph embeddings
//!   weights.rs   the weights file
//!   math.rs      RNG, matrix products, softmax
//!   users.rs     one model per user id: training on request (background thread), the models on disk and in memory
//!   cli.rs       `sargo gcnii train|eval|infer`
//!
//! Inference is also served over HTTP, `POST /v1/gcnii/infer` and `POST /v1/gcnii/embed`, and per-user training with
//! `POST /v1/gcnii/train` and `POST /v1/gcnii/train/status` (see `router.rs`; `[gcnii] weights` for one global model,
//! `[gcnii] models_dir` for the models of users).

pub mod cli;
pub mod dataset;
pub mod embed;
pub mod features;
pub mod graph;
pub mod math;
pub mod metrics;
pub mod model;
pub mod train;
pub mod users;
pub mod weights;
