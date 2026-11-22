//! Input stage: turns a graph into the initial matrix X (the raw node features); X is NOT H0.
//!
//! Rows of X (shape [width][n], row-major), per node:
//!   0                   constant 1
//!   1                   ln(1 + degree)                        (degree without the self loop)
//!   2 .. 2+K            one-hot of the node `type`, over the K types of the vocabulary
//!   2+K                 "unknown type": 1 when the node has no type or a type outside the vocabulary
//! The type set is open: the vocabulary is built from the training graphs (the `MAX_TYPES` most frequent
//! types) and saved in the weights file, so infer/eval encode the types exactly as the training did.

use super::graph::Graph;
use std::collections::HashMap;

/// Raw features of every node that do not depend on the types: the constant 1 and ln(1+degree).
pub const BASE: usize = 2;

/// Most node types kept in the vocabulary (the rest share the "unknown type" feature).
pub const MAX_TYPES: usize = 32;

/// The node types the model knows, in the order of their rows in X.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct TypeVocab {
    types: Vec<String>,
}

impl TypeVocab {
    pub fn new(types: Vec<String>) -> TypeVocab {
        TypeVocab { types }
    }

    /// The `max` most frequent types (ties by name). Nodes without a type are not counted.
    pub fn build<'a>(graphs: impl Iterator<Item = &'a Graph>, max: usize) -> TypeVocab {
        let count = graphs
            .flat_map(|g| g.types.iter())
            .filter(|t| !t.is_empty())
            .fold(HashMap::<&str, usize>::new(), |mut acc, t| {
                *acc.entry(t).or_default() += 1;
                acc
            });
        let mut sorted: Vec<(&str, usize)> = count.into_iter().collect();
        sorted.sort_by(|a, b| b.1.cmp(&a.1).then(a.0.cmp(b.0)));
        TypeVocab {
            types: sorted
                .into_iter()
                .take(max)
                .map(|(t, _)| t.to_string())
                .collect(),
        }
    }

    pub fn types(&self) -> &[String] {
        &self.types
    }

    /// Number of rows of X: BASE + one per type + the "unknown type" row.
    pub fn width(&self) -> usize {
        BASE + self.types.len() + 1
    }

    /// Initial matrix X of shape [width][n] (row-major).
    pub fn initial_matrix(&self, g: &Graph) -> Vec<f32> {
        let n = g.n;
        let unknown = BASE + self.types.len();
        let type_row = |i: usize| {
            self.types
                .iter()
                .position(|t| *t == g.types[i])
                .map_or(unknown, |k| BASE + k)
        };
        let hot: Vec<usize> = (0..n).map(type_row).collect();
        (0..self.width())
            .flat_map(|row| {
                let hot = &hot;
                (0..n).map(move |i| match row {
                    0 => 1.0,
                    1 => (g.degree[i] as f32).ln_1p(),
                    r if hot[i] == r => 1.0,
                    _ => 0.0,
                })
            })
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn graph(types: &[&str]) -> Graph {
        let n = types.len();
        let edges: Vec<(usize, usize)> = (1..n).map(|i| (0, i)).collect(); // a star around node 0
        let mut g = Graph::new(n, &edges).unwrap();
        g.types = types.iter().map(|t| t.to_string()).collect();
        g
    }

    #[test]
    fn vocab_keeps_the_most_frequent_types() {
        let (a, b) = (graph(&["x", "y", "y"]), graph(&["y", "z", "", "x"]));
        let v = TypeVocab::build([&a, &b].into_iter(), 2);
        assert_eq!(v.types(), ["y", "x"]);
        assert_eq!(v.width(), BASE + 2 + 1);
    }

    #[test]
    fn x_has_the_one_hot_and_the_unknown_row() {
        let v = TypeVocab::new(vec!["x".into(), "y".into()]);
        let g = graph(&["x", "y", "other", ""]);
        let (x, n) = (v.initial_matrix(&g), 4);
        assert_eq!(x.len(), 5 * n);
        assert_eq!(&x[..n], [1.0; 4]);
        assert!((x[n] - 3f32.ln_1p()).abs() < 1e-6);
        assert_eq!(&x[2 * n..3 * n], [1.0, 0.0, 0.0, 0.0]);
        assert_eq!(&x[3 * n..4 * n], [0.0, 1.0, 0.0, 0.0]);
        assert_eq!(&x[4 * n..], [0.0, 0.0, 1.0, 1.0]);
    }
}
