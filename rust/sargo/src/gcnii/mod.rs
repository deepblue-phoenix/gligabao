//! GCNII*: a graph classifier (the untied variant of GCNII), trained and run on the CPU. Ported from the
//! deepblue engine (baby_thinker); only the GCNII* model type is here, the weights files are compatible.
//!
//!   model.rs     the model: forward, backward, SGD step (immutable, pure functions)
//!   graph.rs     the graph, its normalized adjacency, the JSON graph document
//!   features.rs  graph -> initial matrix X, node type vocabulary
//!   dataset.rs   loading a training / eval dataset
//!   train.rs     `train`, `evaluate`, `infer`
//!   weights.rs   the weights file
//!   math.rs      RNG, matrix products, softmax
//!   cli.rs       `sargo gcnii train|eval|infer`
//!
//! Inference is also served over HTTP, `POST /v1/gcnii/infer` (see `router.rs`), when `[gcnii] weights` is set.

pub mod cli;
pub mod dataset;
pub mod features;
pub mod graph;
pub mod math;
pub mod model;
pub mod train;
pub mod weights;
