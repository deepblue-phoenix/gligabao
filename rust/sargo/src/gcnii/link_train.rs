//! Training a link-prediction head, the second way to predict links (the first is the cosine of the embeddings of a classification model,
//! see `link.rs`). It runs next to the classification training (`train.rs`), on the same datasets, and the result is one model that can do both.
//!
//! The head scores a pair of nodes from their embeddings (H_depth of the encoder, scaled to unit length): logit = w · (ĥ_u ⊙ ĥ_v) + b,
//! probability sigmoid(logit). Symmetric, `hidden + 1` parameters (`GcniiStar::link`). A new head starts as a scaled cosine scorer, so it starts
//! where the cosine method is and has to improve on it.
//!
//! One training example is one graph, each epoch: a `holdout` fraction of its edges (at least 1) are taken out and become the positive pairs,
//! the encoder runs on the graph WITHOUT them (so it cannot see the answer), and as many random non-edges are the negative pairs; the loss is the
//! binary cross-entropy of the pairs. Then, per `encoder`:
//!
//!   frozen   only the head is trained, on top of the encoder of a model trained for classification (`init`): that model keeps classifying
//!            exactly as before, and gets a head.
//!   train    the encoder is trained too, by the link loss; with `class_weight` > 0 the classification loss of the graph (labels from the
//!            dataset) is added at that weight, so both heads train at once. With 0 the classification head is left as it was (a model
//!            trained from scratch this way is only good for links).
//!
//! `init` may be any model; without it a new one is made (`hidden`, `depth`, the node types of the data).

use super::dataset::Sample;
use super::features::{MAX_TYPES, TypeVocab};
use super::graph::Graph;
use super::math::Rng;
use super::model::{add_grads, GcniiStar, Grads, ModelConfig};
use super::train::TrainControl;
use std::collections::HashSet;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Encoder {
    Frozen,
    Train,
}

impl Encoder {
    pub fn parse(name: &str) -> Result<Encoder, String> {
        match name {
            "frozen" => Ok(Encoder::Frozen),
            "train" => Ok(Encoder::Train),
            other => Err(format!("unknown encoder mode {other:?} (frozen or train)")),
        }
    }
}

pub struct LinkOptions {
    pub epochs: usize,
    pub lr: f32,
    /// Graphs per optimizer step.
    pub batch: usize,
    /// Size of a new model (ignored with `init`).
    pub hidden: usize,
    pub depth: usize,
    pub seed: u64,
    /// The fraction of the edges of a graph taken out as positive pairs, each epoch.
    pub holdout: f32,
    pub encoder: Encoder,
    /// Weight of the classification loss when the encoder is trained (0 = none).
    pub class_weight: f32,
}

/// Positive and negative node pairs `(u, v, label)` of one graph, and the graph the encoder sees (without the positives).
pub struct LinkExample {
    pub graph: Graph,
    pub x: Vec<f32>,
    pub pairs: Vec<(usize, usize, f32)>,
}

/// `count` node pairs that are not edges (of `all`, the full edge set), distinct, a < b. Fewer when the graph is nearly complete.
fn sample_negatives(n: usize, all: &HashSet<(usize, usize)>, count: usize, rng: &mut Rng) -> Vec<(usize, usize)> {
    let free = n * (n - 1) / 2 - all.len();
    let want = count.min(free);
    (0..want * 30 + 30)
        .scan((HashSet::new(), 0usize), |(seen, found), _| {
            if *found >= want {
                return None;
            }
            let (a, b) = (rng.next_u64() as usize % n, rng.next_u64() as usize % n);
            let pair = (a.min(b), a.max(b));
            Some(if a != b && !all.contains(&pair) && seen.insert(pair) {
                *found += 1;
                Some(pair)
            } else {
                None
            })
        })
        .flatten()
        .collect()
}

/// The example of a graph for this epoch (`None` if it has fewer than 2 edges, or no non-edge).
pub fn make_example(model: &GcniiStar, g: &Graph, holdout: f32, rng: &mut Rng) -> Option<LinkExample> {
    let edges = g.edges();
    if edges.len() < 2 || g.n < 3 {
        return None;
    }
    let out = ((edges.len() as f32 * holdout).round() as usize).clamp(1, edges.len() - 1);
    let order = rng.permutation(edges.len());
    let held: Vec<(usize, usize)> = order[..out].iter().map(|&i| edges[i]).collect();
    let kept: Vec<(usize, usize)> = order[out..].iter().map(|&i| edges[i]).collect();
    let all: HashSet<(usize, usize)> = edges.iter().copied().collect();
    let negatives = sample_negatives(g.n, &all, held.len(), rng);
    if negatives.is_empty() {
        return None;
    }
    let seen = Graph {
        names: g.names.clone(),
        types: g.types.clone(),
        ..Graph::new(g.n, &kept).ok()?
    };
    let pairs = held
        .into_iter()
        .map(|(a, b)| (a, b, 1.0))
        .chain(negatives.into_iter().map(|(a, b)| (a, b, 0.0)))
        .collect();
    let x = model.vocab.initial_matrix(&seen);
    Some(LinkExample { graph: seen, x, pairs })
}

/// What one example gives: the mean loss and accuracy (at 0.5) of its pairs, the gradient of the head and, if asked, of the encoder.
pub struct LinkGrads {
    pub loss: f32,
    pub correct: usize,
    pub pairs: usize,
    pub head: Vec<f32>,
    pub encoder: Option<Grads>,
}

fn bce_with_logit(z: f32, y: f32) -> f32 {
    z.max(0.0) - z * y + (-z.abs()).exp().ln_1p()
}

/// Forward and backward of the link loss of one example, every gradient multiplied by `scale` (1 / graphs of the batch).
pub fn link_grads(model: &GcniiStar, ex: &LinkExample, with_encoder: bool, scale: f32) -> Result<LinkGrads, String> {
    let head = model.link_head()?;
    let (hidden, depth) = (model.cfg.hidden, model.cfg.depth);
    let n = ex.graph.n;
    let cache = model.encode(&ex.graph, &ex.x);
    let raw = &cache.h[depth];
    let h = GcniiStar::unit_columns(raw, hidden, n);
    let m = ex.pairs.len() as f32;
    // (loss, correct, head gradient, dĤ: the gradient on the unit embeddings)
    let init = (0.0f32, 0usize, vec![0.0f32; hidden + 1], vec![0.0f32; hidden * n]);
    let (loss, correct, ghead, dunit) = ex.pairs.iter().fold(init, |(loss, correct, mut gh, mut dh), &(u, v, y)| {
        let z = GcniiStar::pair_logit(head, &h, n, u, v);
        let d = (1.0 / (1.0 + (-z).exp()) - y) / m * scale;
        (0..hidden).for_each(|f| {
            gh[f] += d * h[f * n + u] * h[f * n + v];
            dh[f * n + u] += d * head[f] * h[f * n + v];
            dh[f * n + v] += d * head[f] * h[f * n + u];
        });
        gh[hidden] += d;
        (
            loss + bce_with_logit(z, y) / m,
            correct + usize::from((z > 0.0) == (y > 0.5)),
            gh,
            dh,
        )
    });
    // back through the normalization: dh = (dĥ - ĥ (ĥ · dĥ)) / |h|, per node
    let dh: Vec<f32> = (0..hidden * n)
        .map(|i| {
            let j = i % n;
            let norm = (0..hidden).map(|f| raw[f * n + j].powi(2)).sum::<f32>().sqrt();
            if norm > 0.0 {
                let dot: f32 = (0..hidden).map(|f| h[f * n + j] * dunit[f * n + j]).sum();
                (dunit[i] - h[i] * dot) / norm
            } else {
                0.0
            }
        })
        .collect();
    let encoder = with_encoder.then(|| {
        let zero: Grads = model.params.iter().map(|p| vec![0.0; p.len()]).collect();
        model.backward_encoder(&ex.graph, &ex.x, &cache, dh, zero, false).0
    });
    Ok(LinkGrads {
        loss,
        correct,
        pairs: ex.pairs.len(),
        head: ghead,
        encoder,
    })
}

/// Plain SGD on the head (and on the encoder, when `encoder` has gradients).
fn step(model: GcniiStar, head: &[f32], encoder: Option<&Grads>, lr: f32) -> GcniiStar {
    let updated = match encoder {
        Some(g) => model.sgd_step(g, lr),
        None => model,
    };
    let link = updated.link.as_ref().map(|w| w.iter().zip(head).map(|(p, g)| p - lr * g).collect());
    GcniiStar { link, ..updated }
}

/// Trains the link head (see the module docs): returns the model, with its head, or `None` if `ctl` stopped it. The model is `init` (a head
/// is added if it has none, otherwise it is trained on) or a new one. Errors: `frozen` without `init`, `class_weight` with a frozen
/// encoder, a label outside the classes of `init`, no graph with at least 2 edges.
pub fn train_link(
    samples: &[Sample],
    classes: &[String],
    init: Option<GcniiStar>,
    opts: &LinkOptions,
    ctl: Option<&TrainControl>,
) -> Result<Option<GcniiStar>, String> {
    let cancelled = || ctl.is_some_and(|c| c.cancelled());
    if opts.encoder == Encoder::Frozen && init.is_none() {
        return Err("a frozen encoder needs a model to start from (init)".to_string());
    }
    if opts.encoder == Encoder::Frozen && opts.class_weight > 0.0 {
        return Err("class_weight needs the encoder to be trained (encoder = train)".to_string());
    }
    let mut rng = Rng::new(opts.seed);
    let base = match init {
        Some(m) => m,
        None => {
            let vocab = TypeVocab::build(samples.iter().map(|s| &s.graph), MAX_TYPES);
            let cfg = ModelConfig {
                classes: classes.len(),
                hidden: opts.hidden,
                depth: opts.depth,
            };
            GcniiStar::new(cfg, vocab, &mut rng)
        }
    };
    if opts.class_weight > 0.0 {
        if let Some(bad) = samples.iter().find(|s| s.label >= base.cfg.classes) {
            return Err(format!("{}: label {} but the model has {} outputs", bad.path, bad.label, base.cfg.classes));
        }
    }
    let model = if base.link.is_some() { base } else { base.with_new_link_head(&mut rng) };
    if !samples.iter().any(|s| s.graph.edges().len() >= 2 && s.graph.n >= 3) {
        return Err("no graph with at least 2 edges to train link prediction on".to_string());
    }
    println!(
        "link training on CPU (Rust): {}, encoder {:?}, holdout {}, class weight {}",
        model.describe(),
        opts.encoder,
        opts.holdout,
        opts.class_weight
    );
    let with_encoder = opts.encoder == Encoder::Train;
    let batch = opts.batch.max(1);
    let trained = (1..=opts.epochs).try_fold(model, |model, epoch| -> Result<GcniiStar, String> {
        let order = rng.permutation(samples.len());
        // one rng per graph and epoch: the examples do not depend on the order the graphs are visited in
        let seed = opts.seed.wrapping_add(epoch as u64 * 1_000_003);
        let (model, loss, correct, pairs) = order.chunks(batch).try_fold(
            (model, 0.0f32, 0usize, 0usize),
            |(model, loss, correct, pairs), chunk| -> Result<(GcniiStar, f32, usize, usize), String> {
                if cancelled() {
                    return Ok((model, loss, correct, pairs));
                }
                let scale = 1.0 / chunk.len() as f32;
                let grads: Vec<LinkGrads> = chunk
                    .iter()
                    .filter_map(|&i| {
                        let example = make_example(&model, &samples[i].graph, opts.holdout, &mut Rng::new(seed ^ (i as u64 + 1) * 7919))?;
                        Some(link_grads(&model, &example, with_encoder, scale).map(|g| (i, g)))
                    })
                    .collect::<Result<Vec<_>, String>>()?
                    .into_iter()
                    .map(|(i, mut g)| {
                        if with_encoder && opts.class_weight > 0.0 {
                            let s = &samples[i];
                            let x = model.vocab.initial_matrix(&s.graph);
                            let class = model.example_grads(&s.graph, &x, s.label, chunk.len(), false).grads;
                            let weighted: Grads = class.into_iter().map(|t| t.into_iter().map(|v| v * opts.class_weight).collect()).collect();
                            g.encoder = g.encoder.take().map(|e| add_grads(e, &weighted));
                        }
                        g
                    })
                    .collect();
                if grads.is_empty() {
                    return Ok((model, loss, correct, pairs));
                }
                let batch_loss: f32 = grads.iter().map(|g| g.loss).sum();
                let batch_correct: usize = grads.iter().map(|g| g.correct).sum();
                let batch_pairs: usize = grads.iter().map(|g| g.pairs).sum();
                let head = grads.iter().skip(1).fold(grads[0].head.clone(), |acc, g| acc.iter().zip(&g.head).map(|(a, b)| a + b).collect());
                let encoder = if with_encoder {
                    grads.iter().filter_map(|g| g.encoder.clone()).reduce(|a, b| add_grads(a, &b))
                } else {
                    None
                };
                Ok((
                    step(model, &head, encoder.as_ref(), opts.lr),
                    loss + batch_loss,
                    correct + batch_correct,
                    pairs + batch_pairs,
                ))
            },
        )?;
        let mean_loss = loss / samples.len() as f32;
        if let Some(c) = ctl {
            if !c.cancelled() {
                c.report(epoch, mean_loss);
            }
        }
        println!(
            "== link epoch {epoch}/{}: loss {:.4}, pair acc {:.1}%",
            opts.epochs,
            mean_loss,
            100.0 * correct as f32 / pairs.max(1) as f32
        );
        Ok(model)
    })?;
    Ok(if cancelled() { None } else { Some(trained) })
}

#[cfg(test)]
mod tests {
    use super::super::math::softmax_ce;
    use super::super::model::tests::setup;
    use super::*;

    fn opts(encoder: Encoder, class_weight: f32) -> LinkOptions {
        LinkOptions {
            epochs: 30,
            lr: 0.05,
            batch: 2,
            hidden: 6,
            depth: 3,
            seed: 4,
            holdout: 0.3,
            encoder,
            class_weight,
        }
    }

    /// Two communities of 6 joined by one bridge, so held-out edges are predictable from the neighbourhood; two such graphs.
    fn samples() -> Vec<Sample> {
        let clique = |off: usize| -> Vec<(usize, usize)> { (0..6).flat_map(move |i| (i + 1..6).map(move |j| (off + i, off + j))).collect() };
        let edges: Vec<(usize, usize)> = clique(0).into_iter().chain(clique(6)).chain([(0, 6)]).collect();
        (0..2)
            .map(|k| Sample {
                path: format!("g{k}"),
                graph: Graph {
                    types: (0..12).map(|i| format!("t{}", i / 6)).collect(),
                    ..Graph::new(12, &edges).unwrap()
                },
                label: k,
            })
            .collect()
    }

    #[test]
    fn example_hides_the_positive_pairs_from_the_encoder() {
        let (m, g, _) = setup();
        let ex = make_example(&m, &g, 0.3, &mut Rng::new(1)).unwrap();
        let (pos, neg): (Vec<&(usize, usize, f32)>, Vec<&(usize, usize, f32)>) = ex.pairs.iter().partition(|p| p.2 > 0.5);
        assert_eq!(pos.len(), neg.len());
        let seen: HashSet<(usize, usize)> = ex.graph.edges().into_iter().collect();
        let all: HashSet<(usize, usize)> = g.edges().into_iter().collect();
        assert!(pos.iter().all(|p| all.contains(&(p.0, p.1)) && !seen.contains(&(p.0, p.1))));
        assert!(neg.iter().all(|p| !all.contains(&(p.0, p.1)) && p.0 != p.1));
        assert_eq!(seen.len() + pos.len(), all.len());
    }

    #[test]
    fn gradients_match_finite_differences() {
        let (m, g, _) = setup();
        let m = m.with_new_link_head(&mut Rng::new(9));
        let ex = make_example(&m, &g, 0.3, &mut Rng::new(2)).unwrap();
        let grads = link_grads(&m, &ex, true, 1.0).unwrap();
        let loss = |m: &GcniiStar| link_grads(m, &ex, false, 1.0).unwrap().loss;
        let eps = 1e-3;
        let head = grads.head.clone();
        for i in 0..head.len() {
            let (mut up, mut down) = (m.clone(), m.clone());
            up.link.as_mut().unwrap()[i] += eps;
            down.link.as_mut().unwrap()[i] -= eps;
            let numeric = (loss(&up) - loss(&down)) / (2.0 * eps);
            assert!((numeric - head[i]).abs() < 2e-3, "head {i}: {numeric} vs {}", head[i]);
        }
        let enc = grads.encoder.unwrap();
        for t in 0..m.params.len() - 2 {
            for i in (0..m.params[t].len()).step_by(5) {
                let (mut up, mut down) = (m.clone(), m.clone());
                up.params[t][i] += eps;
                down.params[t][i] -= eps;
                let numeric = (loss(&up) - loss(&down)) / (2.0 * eps);
                assert!((numeric - enc[t][i]).abs() < 3e-3, "tensor {t}[{i}]: {numeric} vs {}", enc[t][i]);
            }
        }
        let _ = softmax_ce;
    }

    #[test]
    fn frozen_training_changes_only_the_head() {
        let (s, classes) = (samples(), vec!["a".to_string(), "b".to_string()]);
        let base = train_link(&s, &classes, None, &opts(Encoder::Train, 0.0), None).unwrap().unwrap();
        let plain = GcniiStar { link: None, ..base.clone() };
        let out = train_link(&s, &classes, Some(plain.clone()), &opts(Encoder::Frozen, 0.0), None).unwrap().unwrap();
        assert_eq!(out.params, plain.params, "the classification model is untouched");
        assert!(out.link.is_some());
    }

    #[test]
    fn training_lowers_the_link_loss_and_beats_chance() {
        let (s, classes) = (samples(), vec!["a".to_string(), "b".to_string()]);
        let ctl = TrainControl::default();
        let model = train_link(&s, &classes, None, &opts(Encoder::Train, 0.0), Some(&ctl)).unwrap().unwrap();
        let losses = ctl.epoch_losses();
        assert_eq!(losses.len(), 30);
        assert!(losses[29] < losses[0], "{losses:?}");
        let metrics = super::super::link_eval::compare(&model, &s, &Default::default()).unwrap();
        assert!(metrics["head"]["auc"].as_f64().unwrap() > 0.6, "{metrics}");
    }

    #[test]
    fn joint_training_also_fits_the_classes() {
        let (s, classes) = (samples(), vec!["a".to_string(), "b".to_string()]);
        let model = train_link(&s, &classes, None, &opts(Encoder::Train, 1.0), None).unwrap().unwrap();
        assert!(model.link.is_some() && model.cfg.classes == 2);
    }

    #[test]
    fn bad_options_are_errors_and_cancel_stops() {
        let (s, classes) = (samples(), vec!["a".to_string(), "b".to_string()]);
        assert!(train_link(&s, &classes, None, &opts(Encoder::Frozen, 0.0), None).is_err());
        let (m, _, _) = setup();
        assert!(train_link(&s, &classes, Some(m), &opts(Encoder::Frozen, 1.0), None).is_err());
        let path = vec![Sample {
            path: "p".into(),
            graph: Graph::new(3, &[(0, 1)]).unwrap(),
            label: 0,
        }];
        assert!(train_link(&path, &classes, None, &opts(Encoder::Train, 0.0), None).is_err());
        let ctl = TrainControl::default();
        ctl.cancel.store(true, std::sync::atomic::Ordering::Relaxed);
        assert!(train_link(&s, &classes, None, &opts(Encoder::Train, 0.0), Some(&ctl)).unwrap().is_none());
    }
}
