//! The GCNII* model (the untied variant of GCNII, Chen et al., ICML 2020), CPU only, with hand-written
//! forward and backward passes.
//!
//!   H0   = ReLU(W0 X + b0)                                   input projection of X (`features.rs`), [hidden][N]
//!   A_l  = (1-α) P̃ H_{l-1}                                   propagated neighbors   (P̃ = D̃^-1/2 Ã D̃^-1/2, `Graph::spmm`)
//!   B_l  = α H0                                              initial residual (ego features)
//!   H_l  = ReLU(A_l ((1-β_l) I + β_l W1_l) + B_l ((1-β_l) I + β_l W2_l))     l = 1..depth, β_l = ln(λ/l + 1)
//!   out  = Wo · mean_nodes(H_depth) + bo                     graph embedding -> class logits
//!
//! The neighbor part and the initial residual get SEPARATE weights (W1, W2): the layer equals one wide linear
//! layer on [A ‖ B], so the model can suppress misleading neighbor signal (W1 -> 0) and keep the ego features.
//!
//! Layout: [features][nodes], row-major. The model is immutable: `example_grads` returns the gradients of one
//! example and `sgd_step` returns the updated model (see `train.rs`).
//!
//! Parameter tensors, in this order (the order of the weights file, `weights.rs`): w0, b0, W1 of layers 1..depth,
//! W2 of layers 1..depth, wo, bo.

use super::features::TypeVocab;
use super::graph::Graph;
use super::math::{argmax, matmul, matmul_nt, matmul_tn, softmax_ce, Rng};

pub const ALPHA: f32 = 0.1;
pub const LAMBDA: f32 = 0.5;

/// The initial weight of the link head (see `with_new_link_head`).
pub const LINK_INIT_SCALE: f32 = 6.0;

pub const DEFAULT_HIDDEN: usize = 16;
pub const DEFAULT_DEPTH: usize = 16;

const W0: usize = 0;
const B0: usize = 1;
const CONV: usize = 2;

/// The size of a model; the weights file stores it in its header.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ModelConfig {
    /// Number of outputs (classes).
    pub classes: usize,
    /// Width of the node embeddings (and so the length of the graph embedding).
    pub hidden: usize,
    /// Number of graph layers (message passing steps).
    pub depth: usize,
}

/// β_l = ln(λ / l + 1), the identity-mapping strength of layer l (decays with depth).
fn beta(l: usize) -> f32 {
    (LAMBDA / l as f32 + 1.0).ln()
}

/// Index of the tensor of matrix `kind` (0 = W1, neighbors; 1 = W2, residual) of layer `l` (1-based).
fn conv(depth: usize, kind: usize, l: usize) -> usize {
    CONV + kind * depth + l - 1
}

/// What the forward pass keeps for the backward pass.
pub struct Cache {
    /// h[0] = H0, h[l] = output of layer l.
    pub h: Vec<Vec<f32>>,
    /// a[l-1] = A of layer l (B = α H0 is recomputed from h[0]).
    pub a: Vec<Vec<f32>>,
    /// Graph embedding: the mean over the nodes of H_depth, [hidden].
    pub pooled: Vec<f32>,
    /// Empty after `encode`.
    pub logits: Vec<f32>,
}

/// A GCNII* model.
#[derive(Clone)]
pub struct GcniiStar {
    pub cfg: ModelConfig,
    /// The node types of X; fixes the number of input features. Saved in the weights file.
    pub vocab: TypeVocab,
    pub params: Vec<Vec<f32>>,
    /// The optional link-prediction head (`link_train.rs`): `hidden` weights and a bias. It scores a pair of nodes from their
    /// embeddings, normalized to unit length (`unit_columns`): logit = w · (ĥ_u ⊙ ĥ_v) + b. `None` for a model trained only for classification.
    pub link: Option<Vec<f32>>,
}

/// Gradient of one example: one buffer per parameter tensor, same shapes as `params`.
pub type Grads = Vec<Vec<f32>>;

/// Result of `GcniiStar::example_grads`.
pub struct ExampleGrads {
    pub loss: f32,
    pub correct: bool,
    pub grads: Grads,
    /// The gradient probes, `depth + 1` vectors of [hidden][n] (empty unless asked for): probe 0 is the whole
    /// gradient of H0 (before its ReLU mask), probe l the part layer l adds to it through the initial residual.
    pub probes: Vec<Vec<f32>>,
}

/// Element-wise sum of two gradient sets.
pub fn add_grads(a: Grads, b: &Grads) -> Grads {
    a.into_iter()
        .zip(b)
        .map(|(x, y)| x.iter().zip(y).map(|(p, q)| p + q).collect())
        .collect()
}

impl GcniiStar {
    pub fn new(cfg: ModelConfig, vocab: TypeVocab, rng: &mut Rng) -> GcniiStar {
        let ModelConfig {
            classes,
            hidden,
            depth,
        } = cfg;
        let in_features = vocab.width();
        let mut init = |len: usize, std: f32| -> Vec<f32> {
            (0..len).map(|_| rng.normal() * std).collect()
        };
        let w0 = init(hidden * in_features, (2.0 / in_features as f32).sqrt());
        let convs: Vec<Vec<f32>> = (0..2 * depth)
            .map(|_| init(hidden * hidden, (1.0 / hidden as f32).sqrt()))
            .collect();
        let wo = init(classes * hidden, (2.0 / hidden as f32).sqrt());
        let params = std::iter::once(w0)
            .chain(std::iter::once(vec![0.0; hidden]))
            .chain(convs)
            .chain(std::iter::once(wo))
            .chain(std::iter::once(vec![0.0; classes]))
            .collect();
        GcniiStar {
            cfg,
            vocab,
            params,
            link: None,
        }
    }

    /// The same model with a new, untrained link head. It starts as a scaled cosine scorer (every weight `LINK_INIT_SCALE` plus a little noise,
    /// bias `-LINK_INIT_SCALE / 2`), so training starts from the cosine method and improves on it.
    pub fn with_new_link_head(self, rng: &mut Rng) -> GcniiStar {
        let head = (0..self.cfg.hidden)
            .map(|_| LINK_INIT_SCALE + rng.normal() * 0.1)
            .chain(std::iter::once(-LINK_INIT_SCALE / 2.0))
            .collect();
        GcniiStar {
            link: Some(head),
            ..self
        }
    }

    /// The link head, or an error for a model that has none.
    pub fn link_head(&self) -> Result<&[f32], String> {
        self.link
            .as_deref()
            .ok_or_else(|| "this model has no link head (train one with `sargo gcnii train-link`)".to_string())
    }

    /// Every node embedding (column of a [hidden][n] matrix) scaled to unit length; a zero column stays zero.
    pub fn unit_columns(h: &[f32], hidden: usize, n: usize) -> Vec<f32> {
        let norms: Vec<f32> = (0..n).map(|j| (0..hidden).map(|f| h[f * n + j].powi(2)).sum::<f32>().sqrt()).collect();
        (0..hidden * n)
            .map(|i| if norms[i % n] > 0.0 { h[i] / norms[i % n] } else { 0.0 })
            .collect()
    }

    /// The link logit of the nodes u and v: `head` (hidden weights, then the bias) on the product of their unit embeddings `u_h` ([hidden][n],
    /// see `unit_columns`): w · (û_u ⊙ û_v) + b.
    pub fn pair_logit(head: &[f32], unit_h: &[f32], n: usize, u: usize, v: usize) -> f32 {
        let hidden = head.len() - 1;
        head[hidden] + (0..hidden).map(|f| head[f] * unit_h[f * n + u] * unit_h[f * n + v]).sum::<f32>()
    }

    fn wo(&self) -> usize {
        CONV + 2 * self.cfg.depth
    }

    fn bo(&self) -> usize {
        self.wo() + 1
    }

    /// Number of raw features per node, the rows of X.
    pub fn in_features(&self) -> usize {
        self.vocab.width()
    }

    /// One name per parameter tensor, same order as `params`: `ROW.COL` (`in.W0`, `L03.W2`, `out.Wo`).
    pub fn param_names(&self) -> Vec<String> {
        let depth = self.cfg.depth;
        let layers = (1..=2).flat_map(|kind| (1..=depth).map(move |l| format!("L{l:02}.W{kind}")));
        ["in.W0".to_string(), "in.b0".to_string()]
            .into_iter()
            .chain(layers)
            .chain(["out.Wo".to_string(), "out.bo".to_string()])
            .collect()
    }

    /// One (rows, cols) per parameter tensor, same order as `params` (a vector is (n, 1)).
    pub fn param_shapes(&self) -> Vec<(usize, usize)> {
        let ModelConfig {
            classes,
            hidden,
            depth,
        } = self.cfg;
        [(hidden, self.in_features()), (hidden, 1)]
            .into_iter()
            .chain((0..2 * depth).map(|_| (hidden, hidden)))
            .chain([(classes, hidden), (classes, 1)])
            .collect()
    }

    /// Names of the probes of `ExampleGrads::probes`: `in.dH0` and `Lxx.dH0`.
    pub fn probe_names(&self) -> Vec<String> {
        std::iter::once("in.dH0".to_string())
            .chain((1..=self.cfg.depth).map(|l| format!("L{l:02}.dH0")))
            .collect()
    }

    /// One line for the training log.
    pub fn describe(&self) -> String {
        format!(
            "{} GCNII* layers (untied weights), hidden {}, alpha {ALPHA}, lambda {LAMBDA}",
            self.cfg.depth, self.cfg.hidden
        )
    }

    /// X [in_features][n] -> H0 = ReLU(W0 X + b0) [hidden][n].
    fn h0(&self, x: &[f32], n: usize) -> Vec<f32> {
        let hidden = self.cfg.hidden;
        matmul(&self.params[W0], x, hidden, self.in_features(), n)
            .iter()
            .enumerate()
            .map(|(i, v)| (v + self.params[B0][i / n]).max(0.0))
            .collect()
    }

    //------------------------------------------------------------------------------------------------------------
    // FORWARD
    /// The class logits of a graph (forward pass only). x: the initial matrix X of the graph.
    pub fn logits(&self, g: &Graph, x: &[f32]) -> Vec<f32> {
        self.forward(g, x).logits
    }

    /// The graph embedding (pooled vector before the head), [hidden].
    pub fn embed(&self, g: &Graph) -> Vec<f32> {
        self.encode(g, &self.vocab.initial_matrix(g)).pooled
    }

    /// The embedding of every node (H_depth, before the pooling), [hidden][n] row-major.
    pub fn node_embeddings(&self, g: &Graph) -> Vec<f32> {
        let mut cache = self.encode(g, &self.vocab.initial_matrix(g));
        cache.h.pop().unwrap_or_default()
    }

    pub fn forward(&self, g: &Graph, x: &[f32]) -> Cache {
        let ModelConfig {
            classes, hidden, ..
        } = self.cfg;
        let cache = self.encode(g, x);
        // logits = Wo · pooled + bo
        let logits = matmul(&self.params[self.wo()], &cache.pooled, classes, hidden, 1)
            .iter()
            .zip(&self.params[self.bo()])
            .map(|(v, b)| v + b)
            .collect();
        Cache { logits, ..cache }
    }

    /// Input projection, the layers and the mean pooling (no head).
    pub fn encode(&self, g: &Graph, x: &[f32]) -> Cache {
        let ModelConfig { hidden, depth, .. } = self.cfg;
        let n = g.n;
        let (h, a) = (1..=depth).fold((vec![self.h0(x, n)], Vec::new()), |(mut h, mut a), l| {
            let (out, neighbors) = self.layer(g, &h[0], &h[l - 1], l);
            h.push(out);
            a.push(neighbors);
            (h, a)
        });
        let pooled = (0..hidden)
            .map(|f| h[depth][f * n..(f + 1) * n].iter().sum::<f32>() / n as f32)
            .collect();
        Cache {
            h,
            a,
            pooled,
            logits: Vec::new(),
        }
    }

    /// One layer: returns (H_l, A_l) from H0 and H_{l-1}.
    fn layer(&self, g: &Graph, h0: &[f32], prev: &[f32], l: usize) -> (Vec<f32>, Vec<f32>) {
        let ModelConfig { hidden, depth, .. } = self.cfg;
        let n = g.n;
        let b = beta(l);
        // PROPAGATE + SCALE: A = (1 - α) P̃ H^(l-1)
        let a: Vec<f32> = g.spmm(prev, hidden).iter().map(|v| v * (1.0 - ALPHA)).collect();
        // INITIAL RESIDUAL: B = α H^(0), kept separate from A
        let bres: Vec<f32> = h0.iter().map(|v| ALPHA * v).collect();
        // TRANSFORM: untied weights, W1 for the neighbors A and W2 for the residual B
        let w1a = matmul(&self.params[conv(depth, 0, l)], &a, hidden, hidden, n);
        let w2b = matmul(&self.params[conv(depth, 1, l)], &bres, hidden, hidden, n);
        // IDENTITY MAPPING + RELU
        let out = (0..a.len())
            .map(|i| ((1.0 - b) * (a[i] + bres[i]) + b * (w1a[i] + w2b[i])).max(0.0))
            .collect();
        (out, a)
    }

    //------------------------------------------------------------------------------------------------------------
    // BACKWARD
    /// One training example: forward, softmax cross-entropy, backward. The returned gradient is divided by
    /// `batch_len`, so the gradients of the examples of a batch add up to the gradient of the mean batch loss.
    /// With `with_probes` the gradient probes are returned too (otherwise nothing extra is computed).
    pub fn example_grads(
        &self,
        g: &Graph,
        x: &[f32],
        label: usize,
        batch_len: usize,
        with_probes: bool,
    ) -> ExampleGrads {
        let cache = self.forward(g, x);
        let (loss, dlogits) = softmax_ce(&cache.logits, label);
        let dlogits: Vec<f32> = dlogits.iter().map(|v| v / batch_len as f32).collect();
        let (grads, probes) = self.backward(g, x, &cache, &dlogits, with_probes);
        ExampleGrads {
            loss,
            correct: argmax(&cache.logits) == label,
            grads,
            probes,
        }
    }

    /// The parameter gradients of one example. dlogits: dLoss/dLogits.
    fn backward(
        &self,
        g: &Graph,
        x: &[f32],
        c: &Cache,
        dlogits: &[f32],
        with_probes: bool,
    ) -> (Grads, Vec<Vec<f32>>) {
        let ModelConfig { classes, hidden, .. } = self.cfg;
        let n = g.n;
        let (wo, bo) = (self.wo(), self.bo());
        let mut grads: Grads = self.params.iter().map(|p| vec![0.0; p.len()]).collect();

        // HEAD: dbo = dlogits, dWo = dlogits ⊗ pooled, dpooled = Woᵀ dlogits
        grads[bo] = dlogits.to_vec();
        grads[wo] = matmul(dlogits, &c.pooled, classes, 1, hidden);
        let dpooled = matmul_tn(&self.params[wo], dlogits, classes, hidden, 1);

        // POOLING: every node gets dpooled / n
        let dh: Vec<f32> = (0..hidden * n).map(|i| dpooled[i / n] / n as f32).collect();
        self.backward_encoder(g, x, c, dh, grads, with_probes)
    }

    /// The gradients of the input projection and the layers, given dH of the last layer ([hidden][n]); `grads` already holds
    /// the gradients of the head (zeros when only the encoder is trained, see `link_train.rs`).
    pub fn backward_encoder(
        &self,
        g: &Graph,
        x: &[f32],
        c: &Cache,
        dh: Vec<f32>,
        mut grads: Grads,
        with_probes: bool,
    ) -> (Grads, Vec<Vec<f32>>) {
        let ModelConfig { hidden, depth, .. } = self.cfg;
        let n = g.n;
        let mut probes: Vec<Vec<f32>> = vec![Vec::new(); if with_probes { depth + 1 } else { 0 }];

        // LAYERS, last to first. The state is (dH of the chain path, dH0 collected from the residual paths).
        let bres: Vec<f32> = c.h[0].iter().map(|v| ALPHA * v).collect();
        let (dh, dh0) = (1..=depth)
            .rev()
            .fold((dh, vec![0.0f32; hidden * n]), |(dh, dh0), l| {
                let b = beta(l);
                // RELU MASK: dZ = dH ⊙ 1[H^(l) > 0]
                let dz: Vec<f32> = dh
                    .iter()
                    .zip(&c.h[l])
                    .map(|(d, h)| if *h <= 0.0 { 0.0 } else { *d })
                    .collect();
                let (w1, w2) = (conv(depth, 0, l), conv(depth, 1, l));
                // INPUT GRADIENTS: dA = (1-β) dZ + β W1ᵀ dZ,  dB = (1-β) dZ + β W2ᵀ dZ
                let w1td = matmul_tn(&self.params[w1], &dz, hidden, hidden, n);
                let w2td = matmul_tn(&self.params[w2], &dz, hidden, hidden, n);
                let da: Vec<f32> = (0..dz.len())
                    .map(|i| (1.0 - b) * dz[i] + b * w1td[i])
                    .collect();
                let db: Vec<f32> = (0..dz.len())
                    .map(|i| (1.0 - b) * dz[i] + b * w2td[i])
                    .collect();
                // WEIGHT GRADIENTS: dW1 = β dZ Aᵀ, dW2 = β dZ Bᵀ
                grads[w1] = matmul_nt(&dz, &c.a[l - 1], hidden, n, hidden)
                    .iter()
                    .map(|v| b * v)
                    .collect();
                grads[w2] = matmul_nt(&dz, &bres, hidden, n, hidden)
                    .iter()
                    .map(|v| b * v)
                    .collect();
                // RESIDUAL PATH: dH0 += α dB
                if with_probes {
                    probes[l] = db.iter().map(|d| ALPHA * d).collect(); // PROBE: what layer l adds to dH0
                }
                let dh0 = dh0.iter().zip(&db).map(|(s, d)| s + ALPHA * d).collect();
                // CHAIN PATH: dH^(l-1) = (1-α) P̃ dA
                let next = g.spmm(&da, hidden).iter().map(|v| v * (1.0 - ALPHA)).collect();
                (next, dh0)
            });

        // H0 MERGE: chain gradient + residual gradients, then the ReLU mask of H0
        let merged: Vec<f32> = dh.iter().zip(&dh0).map(|(a, b)| a + b).collect();
        if with_probes {
            probes[0] = merged.clone(); // PROBE: the whole gradient of H0
        }
        let dz0: Vec<f32> = merged
            .iter()
            .zip(&c.h[0])
            .map(|(v, h)| if *h <= 0.0 { 0.0 } else { *v })
            .collect();
        // INPUT PROJECTION: db0 = Σ_nodes dZ0, dW0 = dZ0 Xᵀ
        grads[B0] = (0..hidden)
            .map(|f| dz0[f * n..(f + 1) * n].iter().sum())
            .collect();
        grads[W0] = matmul_nt(&dz0, x, hidden, n, self.in_features());
        (grads, probes)
    }

    /// Plain SGD: returns the model with `params - lr * grads`.
    pub fn sgd_step(self, grads: &Grads, lr: f32) -> GcniiStar {
        let params = self
            .params
            .iter()
            .zip(grads)
            .map(|(p, g)| p.iter().zip(g).map(|(w, d)| w - lr * d).collect())
            .collect();
        GcniiStar { params, ..self }
    }
}

#[cfg(test)]
pub mod tests {
    use super::*;

    pub fn setup() -> (GcniiStar, Graph, Vec<f32>) {
        let cfg = ModelConfig {
            classes: 3,
            hidden: 5,
            depth: 3,
        };
        let vocab = TypeVocab::new(vec!["a".into(), "b".into()]);
        let m = GcniiStar::new(cfg, vocab, &mut Rng::new(3));
        let mut g = Graph::new(5, &[(0, 1), (1, 2), (2, 3), (3, 4), (0, 4), (1, 3)]).unwrap();
        g.types = ["a", "b", "a", "a", "b"].map(String::from).to_vec();
        let x = m.vocab.initial_matrix(&g);
        (m, g, x)
    }

    fn loss(m: &GcniiStar, g: &Graph, x: &[f32]) -> f32 {
        softmax_ce(&m.logits(g, x), 1).0
    }

    #[test]
    fn gradients_match_finite_differences() {
        let (m, g, x) = setup();
        assert_eq!(m.params.len(), 2 + 2 * m.cfg.depth + 2);
        let grads = m.example_grads(&g, &x, 1, 1, false).grads;
        let eps = 1e-3;
        for t in 0..m.params.len() {
            for i in (0..m.params[t].len()).step_by(3) {
                let bumped = |delta: f32| {
                    let mut m2 = m.clone();
                    m2.params[t][i] += delta;
                    loss(&m2, &g, &x)
                };
                let num = (bumped(eps) - bumped(-eps)) / (2.0 * eps);
                assert!(
                    (num - grads[t][i]).abs() < 2e-3 + 0.05 * num.abs(),
                    "tensor {t}[{i}]: numeric {num}, analytic {}",
                    grads[t][i]
                );
            }
        }
    }

    #[test]
    fn names_and_shapes_match_the_params() {
        let (m, _, _) = setup();
        assert_eq!(m.param_names().len(), m.params.len());
        assert!(m
            .param_shapes()
            .iter()
            .zip(&m.params)
            .all(|(&(r, c), p)| r * c == p.len()));
        assert_eq!(m.param_names()[2], "L01.W1");
        assert_eq!(m.param_names()[2 + m.cfg.depth], "L01.W2");
    }

    #[test]
    fn probes_see_every_element() {
        let (m, g, x) = setup();
        assert!(m.example_grads(&g, &x, 1, 1, false).probes.is_empty());
        let probes = m.example_grads(&g, &x, 1, 1, true).probes;
        assert_eq!(probes.len(), m.cfg.depth + 1);
        assert_eq!(m.probe_names().len(), probes.len());
        assert!(probes.iter().all(|p| p.len() == m.cfg.hidden * g.n));
    }

    #[test]
    fn sgd_step_lowers_the_loss() {
        let (m, g, x) = setup();
        let before = loss(&m, &g, &x);
        let grads = m.example_grads(&g, &x, 1, 1, false).grads;
        let after = loss(&m.sgd_step(&grads, 0.05), &g, &x);
        assert!(after < before);
    }
}
