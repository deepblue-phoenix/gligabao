//! Testing link prediction on the node level, the way it is used: a node arrives, some of its links are known, predict the others.
//!
//! For each graph, up to `nodes` nodes with at least 2 neighbours are taken out (chosen by `seed`). Of each, a `reveal` fraction of its
//! neighbours (at least 1, and at least 1 left over) are given as `neighbors` of the new node and the rest are the targets. The model scores the new
//! node against every node of the graph that is not a known neighbour; the targets should come first.
//!
//!   auc          the chance that a random target scores higher than a random non-neighbour (0.5 = random)
//!   mrr          mean reciprocal rank of the best-ranked target
//!   hits_at_10   the fraction of cases with a target among the first 10
//!
//! The same cases score both methods (`link::Method`), so the results are comparable. In-sample for the graphs a model was trained on.

use super::dataset::Sample;
use super::graph::Graph;
use super::link::{score_all, Method, NewNode};
use super::math::Rng;
use super::model::GcniiStar;
use serde_json::{json, Value};

pub struct EvalOptions {
    /// Nodes taken out of each graph.
    pub nodes: usize,
    /// The fraction of the neighbours of a taken-out node that are given as known.
    pub reveal: f32,
    pub seed: u64,
}

impl Default for EvalOptions {
    fn default() -> Self {
        EvalOptions {
            nodes: 5,
            reveal: 0.5,
            seed: 1,
        }
    }
}

/// One test: the graph without the node, the node (type, known neighbours) and the neighbours to find.
pub struct Case {
    pub graph: Graph,
    pub node: NewNode,
    pub targets: Vec<usize>,
}

/// The graph without node `v`; the nodes after it move down by one.
fn remove_node(g: &Graph, v: usize) -> Result<Graph, String> {
    let shift = |i: usize| if i > v { i - 1 } else { i };
    let edges: Vec<(usize, usize)> = g
        .edges()
        .into_iter()
        .filter(|&(a, b)| a != v && b != v)
        .map(|(a, b)| (shift(a), shift(b)))
        .collect();
    let keep = |items: &[String]| -> Vec<String> {
        items
            .iter()
            .enumerate()
            .filter(|&(i, _)| i != v)
            .map(|(_, t)| t.clone())
            .collect()
    };
    Ok(Graph {
        names: keep(&g.names),
        types: keep(&g.types),
        ..Graph::new(g.n - 1, &edges)?
    })
}

fn neighbors_of(g: &Graph, v: usize) -> Vec<usize> {
    g.rows[v].iter().map(|&(j, _)| j).filter(|&j| j != v).collect()
}

/// The cases of one graph.
fn graph_cases(g: &Graph, opts: &EvalOptions, rng: &mut Rng) -> Result<Vec<Case>, String> {
    let eligible: Vec<usize> = (0..g.n).filter(|&v| g.degree[v] >= 2 && g.n >= 4).collect();
    rng.permutation(eligible.len())
        .into_iter()
        .take(opts.nodes)
        .map(|k| {
            let v = eligible[k];
            let nbrs = neighbors_of(g, v);
            let order = rng.permutation(nbrs.len());
            let known_count = ((nbrs.len() as f32 * opts.reveal).round() as usize).clamp(1, nbrs.len() - 1);
            let shift = |i: usize| if i > v { i - 1 } else { i };
            let pick = |range: std::ops::Range<usize>| -> Vec<usize> { order[range].iter().map(|&o| shift(nbrs[o])).collect() };
            Ok(Case {
                graph: remove_node(g, v)?,
                node: NewNode {
                    name: g.names[v].clone(),
                    node_type: g.types[v].clone(),
                    neighbors: pick(0..known_count),
                },
                targets: pick(known_count..nbrs.len()),
            })
        })
        .collect()
}

/// The cases of all the samples (deterministic for the same seed).
pub fn cases(samples: &[Sample], opts: &EvalOptions) -> Result<Vec<Case>, String> {
    let per_graph = samples
        .iter()
        .enumerate()
        .map(|(i, s)| graph_cases(&s.graph, opts, &mut Rng::new(opts.seed.wrapping_add(i as u64 * 7919 + 1))))
        .collect::<Result<Vec<_>, String>>()?;
    let all: Vec<Case> = per_graph.into_iter().flatten().collect();
    if all.is_empty() {
        Err("no graph has a node with at least 2 neighbours (and 4 nodes) to hold out".to_string())
    } else {
        Ok(all)
    }
}

/// (auc, reciprocal rank of the best target, hit in the top 10) of one case; `None` without a candidate that is not a target.
pub fn case_metrics(scores: &[f32], known: &[usize], targets: &[usize]) -> Option<(f32, f32, f32)> {
    let candidates: Vec<usize> = (0..scores.len()).filter(|i| !known.contains(i)).collect();
    let (pos, neg): (Vec<usize>, Vec<usize>) = candidates.iter().partition(|i| targets.contains(i));
    if pos.is_empty() || neg.is_empty() {
        return None;
    }
    let wins: f32 = pos
        .iter()
        .flat_map(|&p| neg.iter().map(move |&q| (p, q)))
        .map(|(p, q)| match scores[p].total_cmp(&scores[q]) {
            std::cmp::Ordering::Greater => 1.0,
            std::cmp::Ordering::Equal => 0.5,
            std::cmp::Ordering::Less => 0.0,
        })
        .sum();
    // pessimistic rank: the candidates scoring at least as high (ties against the target)
    let best_rank = pos
        .iter()
        .map(|&p| 1 + candidates.iter().filter(|&&c| c != p && scores[c] >= scores[p]).count())
        .min()
        .unwrap_or(candidates.len());
    Some((
        wins / (pos.len() * neg.len()) as f32,
        1.0 / best_rank as f32,
        if best_rank <= 10 { 1.0 } else { 0.0 },
    ))
}

/// The mean metrics of `method` over the cases: `{"cases", "auc", "mrr", "hits_at_10"}`.
pub fn evaluate(model: &GcniiStar, cases: &[Case], method: Method) -> Result<Value, String> {
    let results: Vec<(f32, f32, f32)> = cases
        .iter()
        .map(|c| Ok(case_metrics(&score_all(model, &c.graph, &c.node, method)?, &c.node.neighbors, &c.targets)))
        .collect::<Result<Vec<_>, String>>()?
        .into_iter()
        .flatten()
        .collect();
    let mean = |pick: fn(&(f32, f32, f32)) -> f32| results.iter().map(pick).sum::<f32>() / results.len().max(1) as f32;
    Ok(json!({
        "cases": results.len(),
        "auc": mean(|r| r.0),
        "mrr": mean(|r| r.1),
        "hits_at_10": mean(|r| r.2),
    }))
}

/// Both approaches on the same cases: `{"cases", "cosine": {...}, "head": {...} | null}` (`head` is null for a model without a link head).
pub fn compare(model: &GcniiStar, samples: &[Sample], opts: &EvalOptions) -> Result<Value, String> {
    let all = cases(samples, opts)?;
    let head = if model.link.is_some() {
        evaluate(model, &all, Method::Head)?
    } else {
        Value::Null
    };
    Ok(json!({
        "cases": all.len(),
        "cosine": evaluate(model, &all, Method::Cosine)?,
        "head": head,
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn metrics_of_a_perfect_and_a_reversed_ranking() {
        // nodes 0..5; node 0 is known; targets 1, 2
        let perfect = case_metrics(&[0.0, 0.9, 0.8, 0.1, 0.2], &[0], &[1, 2]).unwrap();
        assert_eq!(perfect, (1.0, 1.0, 1.0));
        let reversed = case_metrics(&[0.0, 0.1, 0.2, 0.8, 0.9], &[0], &[1, 2]).unwrap();
        assert_eq!(reversed.0, 0.0);
        assert!((reversed.1 - 1.0 / 3.0).abs() < 1e-6);
        // a tie is half a win and ranks against the target
        let tied = case_metrics(&[0.5, 0.5, 0.5], &[], &[0]).unwrap();
        assert_eq!(tied.0, 0.5);
        assert!((tied.1 - 1.0 / 3.0).abs() < 1e-6);
        assert!(case_metrics(&[0.1, 0.2], &[0], &[1]).is_none(), "no non-target candidate");
    }

    #[test]
    fn cases_take_a_node_out_and_split_its_neighbours() {
        let g = Graph::new(5, &[(0, 1), (0, 2), (0, 3), (3, 4), (1, 2)]).unwrap();
        let samples = vec![Sample {
            path: "g".into(),
            graph: g,
            label: 0,
        }];
        let opts = EvalOptions {
            nodes: 10,
            ..Default::default()
        };
        let all = cases(&samples, &opts).unwrap();
        assert!(!all.is_empty());
        for c in &all {
            assert_eq!(c.graph.n, 4);
            assert!(!c.node.neighbors.is_empty() && !c.targets.is_empty());
            assert!(c.node.neighbors.iter().all(|i| !c.targets.contains(i)));
            assert!(c.node.neighbors.iter().chain(&c.targets).all(|&i| i < 4));
        }
        let again = cases(&samples, &opts).unwrap();
        assert_eq!(again.len(), all.len());
        assert_eq!(again[0].targets, all[0].targets, "the same seed gives the same cases");
    }

    #[test]
    fn a_graph_without_a_holdable_node_is_an_error() {
        let path = Graph::new(3, &[(0, 1), (1, 2)]).unwrap();
        let samples = vec![Sample {
            path: "g".into(),
            graph: path,
            label: 0,
        }];
        assert!(cases(&samples, &EvalOptions::default()).is_err());
    }
}
