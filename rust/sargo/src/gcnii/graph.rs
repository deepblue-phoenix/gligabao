//! The graph, its normalized adjacency, and the JSON graph document.
//!
//! Graph document: `{"n": N, "edges": [[a,b],...], "nodes": [{"name": "...", "type": "..."}, ...]}`
//! (`nodes` optional, one entry per node; `class` is only used by datasets, see `dataset.rs`).

use std::collections::BTreeSet;

/// Renormalized adjacency Â = D^-1/2 (A+I) D^-1/2 as rows of (col, value).
pub struct Graph {
    pub n: usize,
    pub rows: Vec<Vec<(usize, f32)>>,
    /// Number of neighbours of every node (without the self loop).
    pub degree: Vec<usize>,
    /// Name of every node (metadata, not a feature; "" = none).
    pub names: Vec<String>,
    /// Type of every node ("" = none). One-hot encoded into X (`features.rs`).
    pub types: Vec<String>,
}

impl Graph {
    pub fn new(n: usize, edges: &[(usize, usize)]) -> Result<Graph, String> {
        if let Some(&(a, b)) = edges.iter().find(|&&(a, b)| a >= n || b >= n) {
            return Err(format!("edge ({a},{b}) outside nodes 0..{n}"));
        }
        // neighbour sets with the self loops
        let nbrs: Vec<BTreeSet<usize>> = (0..n)
            .map(|i| {
                edges
                    .iter()
                    .filter_map(|&(a, b)| match (a == i, b == i) {
                        (true, _) => Some(b),
                        (_, true) => Some(a),
                        _ => None,
                    })
                    .chain(std::iter::once(i))
                    .collect()
            })
            .collect();
        let rows = nbrs
            .iter()
            .map(|set| {
                set.iter()
                    .map(|&j| (j, 1.0 / ((set.len() * nbrs[j].len()) as f32).sqrt()))
                    .collect()
            })
            .collect();
        Ok(Graph {
            n,
            rows,
            degree: nbrs.iter().map(|s| s.len() - 1).collect(),
            names: vec![String::new(); n],
            types: vec![String::new(); n],
        })
    }

    /// out = Â · h for h, out of shape [f][n] (row-major). Â is symmetric, so this is also Âᵀ · h.
    pub fn spmm(&self, h: &[f32], f: usize) -> Vec<f32> {
        let n = self.n;
        (0..f)
            .flat_map(|k| {
                self.rows
                    .iter()
                    .map(move |row| row.iter().map(|&(j, v)| v * h[k * n + j]).sum())
            })
            .collect()
    }

    /// Builds the graph of a parsed graph document.
    pub fn from_value(v: &serde_json::Value) -> Result<Graph, String> {
        let n = v["n"].as_u64().ok_or("missing n")? as usize;
        let edges: Vec<(usize, usize)> = v["edges"]
            .as_array()
            .ok_or("missing edges")?
            .iter()
            .map(|e| {
                (
                    e[0].as_u64().unwrap_or(u64::MAX) as usize,
                    e[1].as_u64().unwrap_or(u64::MAX) as usize,
                )
            })
            .collect();
        let g = Graph::new(n, &edges)?;
        match v.get("nodes") {
            None | Some(serde_json::Value::Null) => Ok(g),
            Some(nodes) => {
                let nodes = nodes.as_array().ok_or("nodes must be an array")?;
                if nodes.len() != n {
                    return Err(format!("{} nodes entries for n = {n}", nodes.len()));
                }
                let field = |name: &str| -> Result<Vec<String>, String> {
                    nodes
                        .iter()
                        .enumerate()
                        .map(|(i, node)| match node.get(name) {
                            None | Some(serde_json::Value::Null) => Ok(String::new()),
                            Some(serde_json::Value::String(s)) => Ok(s.clone()),
                            Some(_) => Err(format!("nodes[{i}].{name} must be a string")),
                        })
                        .collect()
                };
                Ok(Graph {
                    names: field("name")?,
                    types: field("type")?,
                    ..g
                })
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn adjacency_is_symmetric_and_has_self_loops() {
        let g = Graph::new(3, &[(0, 1), (1, 2)]).unwrap();
        assert_eq!(g.degree, vec![1, 2, 1]);
        let dense = |i: usize, j: usize| g.rows[i].iter().find(|r| r.0 == j).map_or(0.0, |r| r.1);
        assert!((dense(0, 1) - dense(1, 0)).abs() < 1e-7);
        assert!(dense(2, 2) > 0.0);
        assert_eq!(dense(0, 2), 0.0);
    }

    #[test]
    fn bad_edge_is_an_error() {
        assert!(Graph::new(2, &[(0, 5)]).is_err());
    }

    #[test]
    fn document_with_nodes() {
        let v: serde_json::Value = serde_json::from_str(
            r#"{"n":2,"edges":[[0,1]],"nodes":[{"name":"a","type":"x"},{"type":"y"}]}"#,
        )
        .unwrap();
        let g = Graph::from_value(&v).unwrap();
        assert_eq!(g.types, vec!["x", "y"]);
        assert_eq!(g.names, vec!["a", ""]);
    }
}
