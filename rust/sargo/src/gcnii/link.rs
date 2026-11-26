//! Link prediction with a trained model: how likely is a NEW node to be connected to each node of a graph (cluster).
//!
//! The new node is added to the graph (with the neighbours it already has, if any, and its type), the trained
//! model encodes the augmented graph, and every pair (new node, node) is scored from the node embeddings
//! (H_depth, `GcniiStar::node_embeddings`). Two ways to score a pair (`Method`):
//!
//!   cosine  the cosine similarity of the two embeddings, in [0, 1] (H is a ReLU output). Needs nothing but the
//!           classification model: "this node looks like that one in the context of this graph".
//!   head    the link head (`link_train.rs`), a model trained to tell the edges of a graph from the non-edges:
//!           sigmoid(w · (ĥ_u ⊙ ĥ_v) + b) on the unit-length embeddings, a probability. Needs a model that has one (`GcniiStar::link`).
//!
//! `link_eval.rs` measures both on the same held-out nodes. A new node without neighbours only has its type (and
//! degree 0) as features; give it some known neighbours (`neighbors`) for a better signal.

use super::graph::Graph;
use super::model::GcniiStar;
use serde_json::{json, Value};

/// How a pair of nodes is scored.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Method {
    Cosine,
    Head,
}

impl Method {
    pub fn parse(name: &str) -> Result<Method, String> {
        match name {
            "cosine" => Ok(Method::Cosine),
            "head" => Ok(Method::Head),
            other => Err(format!("unknown method {other:?} (cosine or head)")),
        }
    }

    pub fn name(self) -> &'static str {
        match self {
            Method::Cosine => "cosine",
            Method::Head => "head",
        }
    }

    /// The head when the model has one, else the cosine.
    pub fn default_for(model: &GcniiStar) -> Method {
        if model.link.is_some() {
            Method::Head
        } else {
            Method::Cosine
        }
    }
}

/// The node to place in the graph.
#[derive(Default)]
pub struct NewNode {
    pub name: String,
    pub node_type: String,
    /// Indices of the nodes it is already known to be connected to.
    pub neighbors: Vec<usize>,
}

impl NewNode {
    /// `{"name"?, "type"?, "neighbors"?: [idx, ..]}`.
    pub fn from_value(v: &Value) -> Result<NewNode, String> {
        let text = |key: &str| match v.get(key) {
            None | Some(Value::Null) => Ok(String::new()),
            Some(Value::String(s)) => Ok(s.clone()),
            Some(_) => Err(format!("node.{key} must be a string")),
        };
        let neighbors = match v.get("neighbors") {
            None | Some(Value::Null) => Vec::new(),
            Some(list) => list
                .as_array()
                .ok_or("node.neighbors must be an array")?
                .iter()
                .map(|i| i.as_u64().map(|i| i as usize).ok_or("node.neighbors must be node indices"))
                .collect::<Result<_, _>>()?,
        };
        Ok(NewNode {
            name: text("name")?,
            node_type: text("type")?,
            neighbors,
        })
    }
}

/// Cosine similarity of columns `a` and `b` of a [hidden][n] matrix (0 when one is a zero vector).
fn cosine(h: &[f32], n: usize, hidden: usize, a: usize, b: usize) -> f32 {
    let (dot, na, nb) = (0..hidden).fold((0.0f32, 0.0f32, 0.0f32), |(d, x, y), f| {
        let (p, q) = (h[f * n + a], h[f * n + b]);
        (d + p * q, x + p * p, y + q * q)
    });
    if na > 0.0 && nb > 0.0 {
        dot / (na.sqrt() * nb.sqrt())
    } else {
        0.0
    }
}

/// The graph with the new node added as node `n` (its type, its name, an edge to each of its neighbours).
pub fn grow(graph: &Graph, node: &NewNode) -> Result<Graph, String> {
    let n = graph.n;
    if let Some(&bad) = node.neighbors.iter().find(|&&i| i >= n) {
        return Err(format!("neighbor {bad} outside nodes 0..{n}"));
    }
    let edges: Vec<(usize, usize)> = graph
        .edges()
        .into_iter()
        .chain(node.neighbors.iter().map(|&i| (i, n)))
        .collect();
    let grown = Graph::new(n + 1, &edges)?;
    Ok(Graph {
        names: graph.names.iter().cloned().chain([node.name.clone()]).collect(),
        types: graph.types.iter().cloned().chain([node.node_type.clone()]).collect(),
        ..grown
    })
}

/// The score of the new node against each of the `graph.n` nodes, in node order (not sorted).
pub fn score_all(model: &GcniiStar, graph: &Graph, node: &NewNode, method: Method) -> Result<Vec<f32>, String> {
    let n = graph.n;
    let head = match method {
        Method::Head => Some(model.link_head()?),
        Method::Cosine => None,
    };
    let hidden = model.cfg.hidden;
    let h = model.node_embeddings(&grow(graph, node)?);
    let unit = GcniiStar::unit_columns(&h, hidden, n + 1);
    Ok((0..n)
        .map(|i| match head {
            Some(w) => 1.0 / (1.0 + (-GcniiStar::pair_logit(w, &unit, n + 1, n, i)).exp()),
            None => cosine(&h, n + 1, hidden, n, i),
        })
        .collect())
}

/// Scores `node` against every node of `graph` with `method` (None: the head if the model has one, else the cosine); the result is
/// sorted by score, best first, cut to `top_k` when given.
/// `{"method", "node": idx of the new node, "nodes": n, "scores": [{"node", "name"?, "score", "connected"}, ..]}`;
/// `connected` is true for the nodes in `node.neighbors` (the links already known).
pub fn predict(
    model: &GcniiStar,
    graph: &Graph,
    node: &NewNode,
    top_k: Option<usize>,
    method: Option<Method>,
) -> Result<Value, String> {
    let n = graph.n;
    let method = method.unwrap_or_else(|| Method::default_for(model));
    let mut scored: Vec<(usize, f32)> = score_all(model, graph, node, method)?.into_iter().enumerate().collect();
    scored.sort_by(|a, b| b.1.total_cmp(&a.1).then(a.0.cmp(&b.0)));
    scored.truncate(top_k.unwrap_or(n));
    let scores: Vec<Value> = scored
        .into_iter()
        .map(|(i, score)| {
            let mut item = json!({"node": i, "score": score, "connected": node.neighbors.contains(&i)});
            if !graph.names[i].is_empty() {
                item["name"] = json!(graph.names[i]);
            }
            item
        })
        .collect();
    Ok(json!({"method": method.name(), "node": n, "nodes": n, "scores": scores}))
}

#[cfg(test)]
mod tests {
    use super::super::model::tests::setup;
    use super::*;

    #[test]
    fn scores_every_node_sorted_in_unit_range() {
        let (m, g, _) = setup();
        let node = NewNode {
            node_type: "a".into(),
            neighbors: vec![1],
            ..Default::default()
        };
        let out = predict(&m, &g, &node, None, None).unwrap();
        let scores = out["scores"].as_array().unwrap();
        assert_eq!(scores.len(), g.n);
        assert_eq!(out["node"], g.n);
        let values: Vec<f64> = scores.iter().map(|s| s["score"].as_f64().unwrap()).collect();
        assert!(values.iter().all(|v| (0.0..=1.0001).contains(v)));
        assert!(values.windows(2).all(|w| w[0] >= w[1]));
        assert_eq!(scores.iter().filter(|s| s["connected"] == true).count(), 1);
    }

    #[test]
    fn top_k_cuts_and_bad_neighbor_is_an_error() {
        let (m, g, _) = setup();
        let out = predict(&m, &g, &NewNode::default(), Some(2), None).unwrap();
        assert_eq!(out["scores"].as_array().unwrap().len(), 2);
        let bad = NewNode {
            neighbors: vec![99],
            ..Default::default()
        };
        assert!(predict(&m, &g, &bad, None, None).is_err());
    }

    #[test]
    fn head_needs_a_head_and_is_the_default_when_there_is_one() {
        let (m, g, _) = setup();
        let node = NewNode::default();
        assert_eq!(predict(&m, &g, &node, None, None).unwrap()["method"], "cosine");
        assert!(predict(&m, &g, &node, None, Some(Method::Head)).is_err());
        let with = m.with_new_link_head(&mut super::super::math::Rng::new(2));
        let out = predict(&with, &g, &node, None, None).unwrap();
        assert_eq!(out["method"], "head");
        assert!(out["scores"].as_array().unwrap().iter().all(|s| (0.0..=1.0).contains(&s["score"].as_f64().unwrap())));
        assert_eq!(predict(&with, &g, &node, None, Some(Method::Cosine)).unwrap()["method"], "cosine");
    }

    #[test]
    fn edges_roundtrip() {
        let (_, g, _) = setup();
        assert_eq!(Graph::new(g.n, &g.edges()).unwrap().edges(), g.edges());
        assert_eq!(g.edges().len(), 6);
    }
}
