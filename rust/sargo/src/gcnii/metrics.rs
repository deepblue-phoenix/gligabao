//! Gradient metrics of a training run (opt-in, `--grad-stats K` and `--grad-map K` of `sargo gcnii train`; off,
//! nothing here is computed). All of it is pure: every update takes the value and returns the next one, together
//! with the stdout line to print when a record is complete (the caller prints it).
//!
//!   @gradnames N1 N2 ...      once, before the first @grad: the parameter tensors then the probes, `ROW.COL`
//!   @grad STEP NORM:C0,..,C48 ...   the gradient of the optimizer steps STEP-K+1..STEP, one field per tensor of
//!                             @gradnames: the mean L2 norm and the element histogram summed over the K steps
//!                             (C0 = exact zeros, C1..C48 = |g| bins, 4 per decade from 1e-10 to 1e2)
//!   @gradmapshapes NAME:RxC ...   once, before the first @gradmap: name and shape of each tensor (a vector is Nx1);
//!                             the last one, `dH0.map` (hidden x probes), is not a parameter
//!   @gradmap STEP V,V,.. ...  one field per tensor, row-major: the mean |gradient| of every element over the K steps;
//!                             `dH0.map` has per hidden unit and probe the sum of |dH0| over nodes and examples
//!
//! The probes are gradients that are not parameter gradients (`ExampleGrads::probes`): `in.dH0`, the whole gradient
//! of H0, and `Lxx.dH0`, the part layer xx adds to it through the initial residual.

/// Histogram of the individual gradient elements: bin 0 counts exact zeros, bins 1..=GRAD_HIST_BINS cover
/// log10|g| in [GRAD_HIST_MIN_EXP, GRAD_HIST_MAX_EXP) in steps of 1/GRAD_HIST_BINS_PER_DECADE decade (smaller values
/// land in bin 1, larger and NaN/Inf in the last bin).
pub const GRAD_HIST_MIN_EXP: i32 = -10;
pub const GRAD_HIST_MAX_EXP: i32 = 2;
pub const GRAD_HIST_BINS_PER_DECADE: i32 = 4;
pub const GRAD_HIST_BINS: usize =
    ((GRAD_HIST_MAX_EXP - GRAD_HIST_MIN_EXP) * GRAD_HIST_BINS_PER_DECADE) as usize;

/// The histogram (len GRAD_HIST_BINS + 1, see above) of the elements of `g`.
pub fn grad_hist(g: &[f32]) -> Vec<u64> {
    g.iter().fold(vec![0u64; GRAD_HIST_BINS + 1], |mut h, &v| {
        let a = v.abs();
        let bin = if a == 0.0 {
            0
        } else if !a.is_finite() {
            GRAD_HIST_BINS
        } else {
            let i = ((a as f64).log10() - GRAD_HIST_MIN_EXP as f64) * GRAD_HIST_BINS_PER_DECADE as f64;
            1 + (i.floor().max(0.0) as usize).min(GRAD_HIST_BINS - 1)
        };
        h[bin] += 1;
        h
    })
}

fn add_vec<T: Copy + std::ops::Add<Output = T>>(a: &[T], b: &[T]) -> Vec<T> {
    a.iter().zip(b).map(|(x, y)| *x + *y).collect()
}

/// Statistics of the probes of one or more examples: per probe the histogram, the sum of squares and, per hidden
/// unit (row of the [hidden][n] gradient), the sum of |value| over the nodes and examples.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ProbeStats {
    pub hist: Vec<Vec<u64>>,
    pub sumsq: Vec<f64>,
    pub unit: Vec<Vec<f64>>,
}

impl ProbeStats {
    /// The statistics of the probes of one example (`ExampleGrads::probes`); no probes give the empty value.
    pub fn of(probes: &[Vec<f32>], hidden: usize) -> ProbeStats {
        ProbeStats {
            hist: probes.iter().map(|p| grad_hist(p)).collect(),
            sumsq: probes
                .iter()
                .map(|p| p.iter().map(|&v| (v as f64) * (v as f64)).sum())
                .collect(),
            unit: probes
                .iter()
                .map(|p| {
                    let n = p.len() / hidden.max(1);
                    (0..hidden)
                        .map(|f| p[f * n..(f + 1) * n].iter().map(|v| v.abs() as f64).sum())
                        .collect()
                })
                .collect(),
        }
    }

    /// The sum of two statistics (the empty value is the identity).
    pub fn merge(self, other: &ProbeStats) -> ProbeStats {
        if self.hist.is_empty() {
            return other.clone();
        }
        if other.hist.is_empty() {
            return self;
        }
        ProbeStats {
            hist: self.hist.iter().zip(&other.hist).map(|(a, b)| add_vec(a, b)).collect(),
            sumsq: add_vec(&self.sumsq, &other.sumsq),
            unit: self.unit.iter().zip(&other.unit).map(|(a, b)| add_vec(a, b)).collect(),
        }
    }

    /// The L2 norm of every probe.
    pub fn norms(&self) -> Vec<f64> {
        self.sumsq.iter().map(|q| q.sqrt()).collect()
    }
}

/// Gradient statistics (`--grad-stats K`): every K optimizer steps one `@grad` line, the histograms summed over those
/// steps and the norms averaged (a record never spans two epochs: call `flush` at the end of an epoch).
pub struct GradStats {
    every: usize,
    step: usize,
    pending: usize,
    hist: Vec<Vec<u64>>,
    norm: Vec<f64>,
}

impl GradStats {
    /// `names`: the parameter tensors then the probes. Returns the `@gradnames` line to print.
    pub fn new(names: &[String], every: usize) -> (GradStats, String) {
        (
            GradStats {
                every: every.max(1),
                step: 0,
                pending: 0,
                hist: vec![vec![0; GRAD_HIST_BINS + 1]; names.len()],
                norm: vec![0.0; names.len()],
            },
            format!("@gradnames {}", names.join(" ")),
        )
    }

    /// Takes the gradients of one optimizer step (the accumulated gradients of the batch, before `sgd_step`) and the
    /// merged probes of its examples. Returns the next state and, every K steps, the `@grad` line.
    pub fn add(self, grads: &[Vec<f32>], probes: &ProbeStats) -> (GradStats, Option<String>) {
        let hists = grads.iter().map(|g| grad_hist(g)).chain(probes.hist.iter().cloned());
        let norms = grads
            .iter()
            .map(|g| g.iter().map(|&v| (v as f64) * (v as f64)).sum::<f64>().sqrt())
            .chain(probes.norms());
        let next = GradStats {
            step: self.step + 1,
            pending: self.pending + 1,
            hist: self.hist.iter().zip(hists).map(|(a, b)| add_vec(a, &b)).collect(),
            norm: self.norm.iter().zip(norms).map(|(a, b)| a + b).collect(),
            ..self
        };
        if next.pending == next.every {
            next.flush()
        } else {
            (next, None)
        }
    }

    /// The pending steps (if any) as one `@grad` line; starts a new bucket.
    pub fn flush(self) -> (GradStats, Option<String>) {
        if self.pending == 0 {
            return (self, None);
        }
        let fields: Vec<String> = self
            .hist
            .iter()
            .zip(&self.norm)
            .map(|(h, norm)| {
                let counts: Vec<String> = h.iter().map(|c| c.to_string()).collect();
                format!("{:.4e}:{}", norm / self.pending as f64, counts.join(","))
            })
            .collect();
        let line = format!("@grad {} {}", self.step, fields.join(" "));
        (
            GradStats {
                pending: 0,
                hist: self.hist.iter().map(|h| vec![0; h.len()]).collect(),
                norm: vec![0.0; self.norm.len()],
                ..self
            },
            Some(line),
        )
    }
}

/// Per-element gradient map (`--grad-map K`): every K steps one `@gradmap` line with the mean |gradient| of every
/// element of every parameter tensor over those steps (a consumer multiplies by the step count and sums over the run
/// to see how much each element was pushed).
pub struct GradMap {
    every: usize,
    step: usize,
    pending: usize,
    sum: Vec<Vec<f64>>,
}

impl GradMap {
    /// `names`/`shapes`: the parameter tensors, then (when the model has probes) `dH0.map` with shape (hidden, probes).
    /// Returns the `@gradmapshapes` line to print.
    pub fn new(names: &[String], shapes: &[(usize, usize)], every: usize) -> (GradMap, String) {
        let fields: Vec<String> = names
            .iter()
            .zip(shapes)
            .map(|(n, (r, c))| format!("{n}:{r}x{c}"))
            .collect();
        (
            GradMap {
                every: every.max(1),
                step: 0,
                pending: 0,
                sum: shapes.iter().map(|(r, c)| vec![0.0; r * c]).collect(),
            },
            format!("@gradmapshapes {}", fields.join(" ")),
        )
    }

    /// Adds |g| of one optimizer step. `units` ([probe][hidden unit], `ProbeStats::unit`, may be empty) fill the last
    /// tensor, a [hidden][probes] map. Returns the next state and, every K steps, the `@gradmap` line.
    pub fn add(self, grads: &[Vec<f32>], units: &[Vec<f64>]) -> (GradMap, Option<String>) {
        let probes = units.len();
        let sum = self
            .sum
            .iter()
            .enumerate()
            .map(|(t, acc)| match grads.get(t) {
                Some(g) => acc.iter().zip(g).map(|(a, v)| a + v.abs() as f64).collect(),
                // the dH0 map: [hidden][probe]
                None => acc
                    .iter()
                    .enumerate()
                    .map(|(i, a)| a + units.get(i % probes.max(1)).map_or(0.0, |col| col[i / probes.max(1)]))
                    .collect(),
            })
            .collect();
        let next = GradMap {
            step: self.step + 1,
            pending: self.pending + 1,
            sum,
            ..self
        };
        if next.pending == next.every {
            next.flush()
        } else {
            (next, None)
        }
    }

    /// The pending steps (if any) as one `@gradmap STEP v,v,.. v,v,..` line; starts anew.
    pub fn flush(self) -> (GradMap, Option<String>) {
        if self.pending == 0 {
            return (self, None);
        }
        let n = self.pending as f64;
        let fields: Vec<String> = self
            .sum
            .iter()
            .map(|acc| {
                acc.iter()
                    .map(|v| format!("{:.3e}", v / n))
                    .collect::<Vec<_>>()
                    .join(",")
            })
            .collect();
        let line = format!("@gradmap {} {}", self.step, fields.join(" "));
        (
            GradMap {
                pending: 0,
                sum: self.sum.iter().map(|a| vec![0.0; a.len()]).collect(),
                ..self
            },
            Some(line),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hist_bins() {
        let h = grad_hist(&[0.0, 1e-12, 1.0, 1e5, f32::NAN, -1.0]);
        assert_eq!(h[0], 1);
        assert_eq!(h[1], 1);
        assert_eq!(h[1 + 40], 2); // 1.0 and -1.0: log10 = 0 -> (0+10)*4 = 40
        assert_eq!(h[GRAD_HIST_BINS], 2);
    }

    #[test]
    fn probe_stats_merge_and_identity() {
        let p = ProbeStats::of(&[vec![1.0, -2.0, 0.0, 3.0]], 2); // hidden 2, n 2
        assert_eq!(p.unit, vec![vec![3.0, 3.0]]);
        let both = ProbeStats::default().merge(&p).merge(&p);
        assert_eq!(both.unit, vec![vec![6.0, 6.0]]);
        assert_eq!(both.hist[0].iter().sum::<u64>(), 8);
        assert!((both.norms()[0] - (28.0f64).sqrt()).abs() < 1e-9);
    }

    #[test]
    fn stats_bucket_the_steps() {
        let (gs, header) = GradStats::new(&["a.W".to_string()], 2);
        assert_eq!(header, "@gradnames a.W");
        let none = ProbeStats::default();
        let (gs, line) = gs.add(&[vec![1.0, 0.0]], &none);
        assert!(line.is_none());
        let (gs, line) = gs.add(&[vec![1.0, 0.0]], &none);
        assert!(line.unwrap().starts_with("@grad 2 "));
        assert_eq!(gs.pending, 0);
        let (_, line) = gs.flush();
        assert!(line.is_none());
    }

    #[test]
    fn map_averages_the_steps_and_has_the_unit_map() {
        let names = ["a.W".to_string(), "dH0.map".to_string()];
        let (gm, header) = GradMap::new(&names, &[(1, 2), (2, 1)], 2);
        assert_eq!(header, "@gradmapshapes a.W:1x2 dH0.map:2x1");
        let units = vec![vec![1.0, 2.0]]; // one probe, two hidden units
        let (gm, line) = gm.add(&[vec![-1.0, 0.0]], &units);
        assert!(line.is_none());
        let (_, line) = gm.add(&[vec![3.0, 0.0]], &units);
        assert_eq!(line.unwrap(), "@gradmap 2 2.000e0,0.000e0 1.000e0,2.000e0");
    }
}
