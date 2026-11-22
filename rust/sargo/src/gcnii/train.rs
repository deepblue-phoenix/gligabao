//! Training, evaluation and inference of the GCNII* model.
//!
//! Training is plain mini-batch SGD with softmax cross-entropy. The pieces are pure functions over the
//! immutable `GcniiStar`: the gradients of a batch are the sum of `example_grads` (each already divided by the
//! batch size) and `sgd_step` returns the next model; the epochs and batches are a fold over the models.
//! Only the shuffle uses the (mutable) `Rng`.

use super::dataset::Sample;
use super::features::{TypeVocab, MAX_TYPES};
use super::graph::Graph;
use super::math::{argmax, softmax, softmax_ce, Rng};
use super::model::{add_grads, GcniiStar, ModelConfig};
use serde_json::json;
use std::time::Instant;

pub struct TrainOptions {
    pub epochs: usize,
    pub lr: f32,
    pub batch: usize,
    pub hidden: usize,
    pub depth: usize,
    pub seed: u64,
}

/// A sample with its initial matrix X.
struct Featured<'a> {
    sample: &'a Sample,
    x: Vec<f32>,
}

fn featured<'a>(samples: &'a [Sample], vocab: &TypeVocab) -> Vec<Featured<'a>> {
    samples
        .iter()
        .map(|sample| Featured {
            sample,
            x: vocab.initial_matrix(&sample.graph),
        })
        .collect()
}

/// (summed loss, number correct) over one batch, after applying one SGD step: returns the next model too.
fn train_batch(model: GcniiStar, batch: &[&Featured], lr: f32) -> (GcniiStar, f32, usize) {
    let results: Vec<_> = batch
        .iter()
        .map(|f| model.example_grads(&f.sample.graph, &f.x, f.sample.label, batch.len()))
        .collect();
    let loss: f32 = results.iter().map(|r| r.loss).sum();
    let correct = results.iter().filter(|r| r.correct).count();
    let grads = results
        .into_iter()
        .map(|r| r.grads)
        .reduce(|a, b| add_grads(a, &b))
        .expect("a batch has at least one example");
    (model.sgd_step(&grads, lr), loss, correct)
}

/// Trains a new model on the samples. `classes`: the class names (one output each).
pub fn train(samples: &[Sample], classes: &[String], opts: &TrainOptions) -> GcniiStar {
    let vocab = TypeVocab::build(samples.iter().map(|s| &s.graph), MAX_TYPES);
    println!(
        "node types: {} in the vocabulary (max {MAX_TYPES}) {:?}, {} input features",
        vocab.types().len(),
        vocab.types(),
        vocab.width()
    );
    let mut rng = Rng::new(opts.seed);
    let cfg = ModelConfig {
        classes: classes.len(),
        hidden: opts.hidden,
        depth: opts.depth,
    };
    let model = GcniiStar::new(cfg, vocab.clone(), &mut rng);
    println!("training on CPU (Rust): {}", model.describe());
    let data = featured(samples, &vocab);
    let batch = opts.batch.max(1);
    let start = Instant::now();
    (1..=opts.epochs).fold(model, |model, epoch| {
        let epoch_start = Instant::now();
        let order = rng.permutation(data.len());
        let (model, loss, correct) = order.chunks(batch).fold(
            (model, 0.0f32, 0usize),
            |(model, loss, correct), chunk| {
                let items: Vec<&Featured> = chunk.iter().map(|&i| &data[i]).collect();
                let (next, batch_loss, batch_correct) = train_batch(model, &items, opts.lr);
                (next, loss + batch_loss, correct + batch_correct)
            },
        );
        let n = data.len() as f32;
        println!(
            "== epoch {epoch}/{}: loss {:.4}, acc {:.1}%, epoch {:.1?}, total {:.1?}",
            opts.epochs,
            loss / n,
            100.0 * correct as f32 / n,
            epoch_start.elapsed(),
            start.elapsed()
        );
        model
    })
}

/// Accuracy (0..1) and mean loss of the model over the samples.
pub fn evaluate(model: &GcniiStar, samples: &[Sample]) -> Result<(f32, f32), String> {
    let results = samples
        .iter()
        .map(|s| {
            let logits = model.logits(&s.graph, &model.vocab.initial_matrix(&s.graph));
            if logits.len() <= s.label {
                return Err(format!(
                    "{}: label {} but the model has {} outputs",
                    s.path,
                    s.label,
                    logits.len()
                ));
            }
            Ok((softmax_ce(&logits, s.label).0, argmax(&logits) == s.label))
        })
        .collect::<Result<Vec<_>, String>>()?;
    let n = results.len() as f32;
    Ok((
        results.iter().filter(|r| r.1).count() as f32 / n,
        results.iter().map(|r| r.0).sum::<f32>() / n,
    ))
}

/// The inference result of one graph as JSON: `class` (name if `classes` has it, else the index), `index`,
/// `logits`, `probabilities` (a map class -> p when `classes` is given) and the graph size.
pub fn infer(model: &GcniiStar, graph: &Graph, classes: &[String]) -> serde_json::Value {
    let logits = model.logits(graph, &model.vocab.initial_matrix(graph));
    let probs = softmax(&logits);
    let best = argmax(&logits);
    let name = |i: usize| classes.get(i).cloned().unwrap_or_else(|| i.to_string());
    json!({
        "class": name(best),
        "index": best,
        "logits": logits,
        "probabilities": (0..probs.len()).map(|i| (name(i), json!(probs[i]))).collect::<serde_json::Map<_, _>>(),
        "nodes": graph.n,
        "edges": graph.degree.iter().sum::<usize>() / 2,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Two easily separable classes: a path (sparse) and a clique (dense).
    fn samples() -> (Vec<Sample>, Vec<String>) {
        let make = |n: usize, star: bool, label: usize| {
            let edges: Vec<(usize, usize)> = (0..n)
                .flat_map(|i| (i + 1..n).map(move |j| (i, j)))
                .filter(|&(i, j)| star || j == i + 1)
                .collect();
            Sample {
                path: format!("g{n}_{label}"),
                graph: Graph::new(n, &edges).unwrap(),
                label,
            }
        };
        let all = (4..10)
            .flat_map(|n| vec![make(n, false, 0), make(n, true, 1)])
            .collect();
        (all, vec!["path".to_string(), "clique".to_string()])
    }

    #[test]
    fn learns_a_toy_problem() {
        let (data, classes) = samples();
        let opts = TrainOptions {
            epochs: 200,
            lr: 0.05,
            batch: 4,
            hidden: 8,
            depth: 3,
            seed: 7,
        };
        let model = train(&data, &classes, &opts);
        let (acc, _) = evaluate(&model, &data).unwrap();
        assert!(acc > 0.9, "accuracy {acc}");
        let out = infer(&model, &data[1].graph, &classes);
        assert_eq!(out["class"], "clique");
    }
}
