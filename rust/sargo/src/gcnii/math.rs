//! Numeric helpers of the GCNII* model: the RNG, the row-major matrix products and the softmax cross-entropy.

/// Tiny xorshift RNG (no dependency). `next_u64` mutates the state; it is only used for the weight
/// initialisation and the shuffle of the training data.
pub struct Rng(u64);

impl Rng {
    pub fn new(seed: u64) -> Rng {
        Rng(seed.wrapping_mul(0x9E3779B97F4A7C15) | 1)
    }

    pub fn next_u64(&mut self) -> u64 {
        self.0 ^= self.0 << 13;
        self.0 ^= self.0 >> 7;
        self.0 ^= self.0 << 17;
        self.0
    }

    pub fn uniform(&mut self) -> f32 {
        ((self.next_u64() >> 40) as f32 + 0.5) / (1u64 << 24) as f32
    }

    pub fn normal(&mut self) -> f32 {
        let (u1, u2) = (self.uniform(), self.uniform());
        (-2.0 * u1.ln()).sqrt() * (2.0 * std::f32::consts::PI * u2).cos()
    }

    /// Fisher-Yates shuffle of `0..len`.
    pub fn permutation(&mut self, len: usize) -> Vec<usize> {
        (1..len).rev().fold((0..len).collect::<Vec<usize>>(), |mut order, i| {
            let j = self.next_u64() as usize % (i + 1);
            order.swap(i, j);
            order
        })
    }
}

/// c[m][n] = a[m][k] · b[k][n]
pub fn matmul(a: &[f32], b: &[f32], m: usize, k: usize, n: usize) -> Vec<f32> {
    (0..m)
        .flat_map(|i| {
            (0..n).map(move |j| (i, j))
        })
        .map(|(i, j)| (0..k).map(|p| a[i * k + p] * b[p * n + j]).sum())
        .collect()
}

/// c[k][n] = aᵀ · b for a[m][k], b[m][n]
pub fn matmul_tn(a: &[f32], b: &[f32], m: usize, k: usize, n: usize) -> Vec<f32> {
    (0..k)
        .flat_map(|p| {
            (0..n).map(move |j| (p, j))
        })
        .map(|(p, j)| (0..m).map(|i| a[i * k + p] * b[i * n + j]).sum())
        .collect()
}

/// c[m][k] = a · bᵀ for a[m][n], b[k][n]
pub fn matmul_nt(a: &[f32], b: &[f32], m: usize, n: usize, k: usize) -> Vec<f32> {
    (0..m)
        .flat_map(|i| {
            (0..k).map(move |p| (i, p))
        })
        .map(|(i, p)| (0..n).map(|j| a[i * n + j] * b[p * n + j]).sum())
        .collect()
}

/// Softmax of the logits.
pub fn softmax(logits: &[f32]) -> Vec<f32> {
    let max = logits.iter().cloned().fold(f32::MIN, f32::max);
    let exps: Vec<f32> = logits.iter().map(|l| (l - max).exp()).collect();
    let sum: f32 = exps.iter().sum();
    exps.iter().map(|e| e / sum).collect()
}

/// Softmax cross-entropy: returns (loss, dLoss/dLogits = softmax - onehot(label)).
pub fn softmax_ce(logits: &[f32], label: usize) -> (f32, Vec<f32>) {
    let probs = softmax(logits);
    let loss = -probs[label].max(1e-12).ln();
    let grad = probs
        .iter()
        .enumerate()
        .map(|(i, p)| if i == label { p - 1.0 } else { *p })
        .collect();
    (loss, grad)
}

pub fn argmax(v: &[f32]) -> usize {
    (0..v.len()).fold(0, |best, i| if v[i] > v[best] { i } else { best })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn matmul_variants_agree() {
        let a = [1.0, 2.0, 3.0, 4.0, 5.0, 6.0]; // 2x3
        let b = [1.0, 0.0, 2.0, 1.0, 0.0, 3.0]; // 3x2
        let c = matmul(&a, &b, 2, 3, 2);
        assert_eq!(c, vec![5.0, 11.0, 14.0, 23.0]);
        // aᵀ·c (3x2) = matmul_tn; check against transposing by hand
        let at = [1.0, 4.0, 2.0, 5.0, 3.0, 6.0];
        assert_eq!(matmul_tn(&a, &c, 2, 3, 2), matmul(&at, &c, 3, 2, 2));
        // a·bᵀ for b given as 2x3
        let bt = [1.0, 2.0, 0.0, 0.0, 1.0, 3.0];
        assert_eq!(matmul_nt(&a, &bt, 2, 3, 2), c);
    }

    #[test]
    fn softmax_ce_gradient_sums_to_zero() {
        let (loss, grad) = softmax_ce(&[0.1, 2.0, -1.0], 1);
        assert!(loss > 0.0);
        assert!(grad.iter().sum::<f32>().abs() < 1e-6);
    }
}
