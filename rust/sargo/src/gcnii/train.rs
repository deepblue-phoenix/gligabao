//! Training, evaluation and inference of the GCNII* model.
//!
//! Training is plain mini-batch SGD with softmax cross-entropy. The pieces are pure functions over the
//! immutable `GcniiStar`: the gradients of a batch are the sum of `example_grads` (each already divided by the
//! batch size) and `sgd_step` returns the next model; the epochs and batches are a fold over the models.
//! Only the shuffle uses the (mutable) `Rng`.

use super::dataset::Sample;
use super::features::{TypeVocab, MAX_TYPES};
use super::graph::Graph;
use super::metrics::{GradMap, GradStats, ProbeStats};
use super::math::{argmax, softmax, softmax_ce, Rng};
use super::model::{add_grads, GcniiStar, ModelConfig};
use serde_json::json;
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::Mutex;
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicUsize, Ordering};
use std::time::Instant;

/// Shared between a training thread and whoever watches or stops it: the training reports its progress here (the last finished epoch
/// and its mean loss) and checks `cancel` between batches.
pub struct TrainControl {
    pub cancel: AtomicBool,
    epoch: AtomicUsize,
    loss_bits: AtomicU32,
    // in-memory queue of the mean training loss of every finished epoch: the training thread only pushes (an unbounded channel never
    // blocks the sender), the watchers drain it into `seen` when they ask, so training never waits for a consumer
    queue: Sender<f32>,
    drain: Mutex<(Receiver<f32>, Vec<f32>)>, // (the queue's receiving end, the losses drained so far: index = epoch - 1)
}

impl Default for TrainControl {
    fn default() -> Self {
        let (queue, rx) = channel();
        TrainControl {
            cancel: AtomicBool::new(false),
            epoch: AtomicUsize::new(0),
            loss_bits: AtomicU32::new(0),
            queue,
            drain: Mutex::new((rx, Vec::new())),
        }
    }
}

impl TrainControl {
    pub fn cancelled(&self) -> bool {
        self.cancel.load(Ordering::Relaxed)
    }
    /// The last finished epoch (0 before the first) and its mean training loss.
    pub fn progress(&self) -> (usize, f32) {
        (self.epoch.load(Ordering::Relaxed), f32::from_bits(self.loss_bits.load(Ordering::Relaxed)))
    }
    /// The mean training loss of every finished epoch so far, in order (epoch 1 first).
    pub fn epoch_losses(&self) -> Vec<f32> {
        let mut guard = self.drain.lock().unwrap_or_else(|e| e.into_inner());
        let (rx, seen) = &mut *guard;
        seen.extend(rx.try_iter());
        seen.clone()
    }
    pub fn report(&self, epoch: usize, loss: f32) {
        let _ = self.queue.send(loss); // never blocks; the receiver lives as long as this struct
        self.loss_bits.store(loss.to_bits(), Ordering::Relaxed);
        self.epoch.store(epoch, Ordering::Relaxed);
    }
}

pub struct TrainOptions {
    pub epochs: usize,
    pub lr: f32,
    pub batch: usize,
    pub hidden: usize,
    pub depth: usize,
    pub seed: u64,
    /// `--grad-stats K`: one `@grad` line per K steps (None = off, nothing is computed).
    pub grad_stats: Option<usize>,
    /// `--grad-map K`: one `@gradmap` line per K steps (None = off).
    pub grad_map: Option<usize>,
}

/// The opt-in gradient metrics of a run (see `metrics.rs`); each update returns the next value and prints the
/// finished records.
struct Monitor {
    stats: Option<GradStats>,
    map: Option<GradMap>,
}

fn print_line(line: Option<String>) {
    if let Some(l) = line {
        println!("{l}");
    }
}

impl Monitor {
    /// Prints the header lines (`@gradnames`, `@gradmapshapes`).
    fn new(model: &GcniiStar, opts: &TrainOptions) -> Monitor {
        let probe_names = model.probe_names();
        let stats = opts.grad_stats.map(|k| {
            let names: Vec<String> = model.param_names().into_iter().chain(probe_names.clone()).collect();
            let (gs, header) = GradStats::new(&names, k);
            println!("{header}");
            gs
        });
        let map = opts.grad_map.map(|k| {
            let names: Vec<String> = model
                .param_names()
                .into_iter()
                .chain(std::iter::once("dH0.map".to_string()))
                .collect();
            let shapes: Vec<(usize, usize)> = model
                .param_shapes()
                .into_iter()
                .chain(std::iter::once((model.cfg.hidden, probe_names.len())))
                .collect();
            let (gm, header) = GradMap::new(&names, &shapes, k);
            println!("{header}");
            gm
        });
        Monitor { stats, map }
    }

    fn enabled(&self) -> bool {
        self.stats.is_some() || self.map.is_some()
    }

    /// One optimizer step: the summed gradients of the batch and the merged probes of its examples.
    fn on_step(self, grads: &[Vec<f32>], probes: &ProbeStats) -> Monitor {
        Monitor {
            stats: self.stats.map(|gs| {
                let (next, line) = gs.add(grads, probes);
                print_line(line);
                next
            }),
            map: self.map.map(|gm| {
                let (next, line) = gm.add(grads, &probes.unit);
                print_line(line);
                next
            }),
        }
    }

    /// A @grad record never spans two epochs.
    fn end_epoch(self) -> Monitor {
        Monitor {
            stats: self.stats.map(|gs| {
                let (next, line) = gs.flush();
                print_line(line);
                next
            }),
            ..self
        }
    }

    fn finish(self) {
        let flushed = self.end_epoch();
        if let Some(gm) = flushed.map {
            print_line(gm.flush().1);
        }
    }
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

/// One optimizer step over a batch: returns the next model, the summed loss, the number correct and the monitor.
fn train_batch(model: GcniiStar, batch: &[&Featured], lr: f32, monitor: Monitor) -> (GcniiStar, f32, usize, Monitor) {
    let with_probes = monitor.enabled();
    let results: Vec<_> = batch
        .iter()
        .map(|f| model.example_grads(&f.sample.graph, &f.x, f.sample.label, batch.len(), with_probes))
        .collect();
    let hidden = model.cfg.hidden;
    let probes = results
        .iter()
        .map(|r| ProbeStats::of(&r.probes, hidden))
        .fold(ProbeStats::default(), |acc, p| acc.merge(&p));
    let loss: f32 = results.iter().map(|r| r.loss).sum();
    let correct = results.iter().filter(|r| r.correct).count();
    let grads = results
        .into_iter()
        .map(|r| r.grads)
        .reduce(|a, b| add_grads(a, &b))
        .expect("a batch has at least one example");
    let monitor = monitor.on_step(&grads, &probes);
    (model.sgd_step(&grads, lr), loss, correct, monitor)
}

/// Trains a new model on the samples. `classes`: the class names (one output each).
pub fn train(samples: &[Sample], classes: &[String], opts: &TrainOptions) -> GcniiStar {
    train_with(samples, classes, opts, None).expect("a training without a control is never cancelled")
}

/// Like `train`, reporting the progress to `ctl` and stopping (`None`) as soon as `ctl.cancel` is set (at the next batch).
pub fn train_with(
    samples: &[Sample],
    classes: &[String],
    opts: &TrainOptions,
    ctl: Option<&TrainControl>,
) -> Option<GcniiStar> {
    let cancelled = || ctl.is_some_and(|c| c.cancelled());
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
    let monitor = Monitor::new(&model, opts);
    let (model, monitor) = (1..=opts.epochs).fold((model, monitor), |(model, monitor), epoch| {
        let epoch_start = Instant::now();
        let order = rng.permutation(data.len());
        let (model, loss, correct, monitor) = order.chunks(batch).fold(
            (model, 0.0f32, 0usize, monitor),
            |(model, loss, correct, monitor), chunk| {
                if cancelled() {
                    return (model, loss, correct, monitor);
                }
                let items: Vec<&Featured> = chunk.iter().map(|&i| &data[i]).collect();
                let (next, batch_loss, batch_correct, monitor) = train_batch(model, &items, opts.lr, monitor);
                (next, loss + batch_loss, correct + batch_correct, monitor)
            },
        );
        let monitor = monitor.end_epoch();
        let n = data.len() as f32;
        if let Some(c) = ctl {
            if !c.cancelled() {
                c.report(epoch, loss / n);
            }
        }
        println!(
            "== epoch {epoch}/{}: loss {:.4}, acc {:.1}%, epoch {:.1?}, total {:.1?}",
            opts.epochs,
            loss / n,
            100.0 * correct as f32 / n,
            epoch_start.elapsed(),
            start.elapsed()
        );
        (model, monitor)
    });
    monitor.finish();
    if cancelled() {
        None
    } else {
        Some(model)
    }
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
            grad_stats: Some(2),
            grad_map: Some(3),
        };
        let model = train(&data, &classes, &opts);
        let (acc, _) = evaluate(&model, &data).unwrap();
        assert!(acc > 0.9, "accuracy {acc}");
        let out = infer(&model, &data[1].graph, &classes);
        assert_eq!(out["class"], "clique");
    }
}
