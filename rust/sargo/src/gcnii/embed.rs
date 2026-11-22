//! Graph embeddings for vector similarity: the forward pass of a trained model up to (not including) the
//! classification head, the mean over the nodes of H_depth: `hidden` floats, the same length for graphs of any size.

use super::dataset::Sample;
use super::model::GcniiStar;
use std::fs;
use std::io::Write;
use std::path::Path;

/// Scales v to unit length (so the dot product is the cosine similarity). A zero vector stays zero.
pub fn l2_normalize(v: &[f32]) -> Vec<f32> {
    let norm = v.iter().map(|x| x * x).sum::<f32>().sqrt();
    if norm > 0.0 {
        v.iter().map(|x| x / norm).collect()
    } else {
        v.to_vec()
    }
}

/// The embedding of one graph, optionally normalized to unit length.
pub fn embed(model: &GcniiStar, graph: &super::graph::Graph, normalize: bool) -> Vec<f32> {
    let e = model.embed(graph);
    if normalize {
        l2_normalize(&e)
    } else {
        e
    }
}

/// One line of the export: the graph file, its class (when known), its node names and its embedding.
pub struct Entry<'a> {
    pub path: &'a str,
    pub class: Option<&'a str>,
    /// Only written when at least one node has a name.
    pub names: &'a [String],
    pub embedding: Vec<f32>,
}

impl Entry<'_> {
    /// `{"path", "class"?, "names"?, "embedding"}`.
    pub fn to_json(&self) -> serde_json::Value {
        let base = serde_json::json!({ "path": self.path, "embedding": self.embedding });
        let with_class = match self.class {
            Some(c) => merge(base, "class", c.into()),
            None => base,
        };
        if self.names.iter().any(|n| !n.is_empty()) {
            merge(with_class, "names", self.names.into())
        } else {
            with_class
        }
    }
}

fn merge(mut obj: serde_json::Value, key: &str, value: serde_json::Value) -> serde_json::Value {
    obj[key] = value;
    obj
}

/// Writes the entries as JSON lines.
pub fn write_jsonl(path: &Path, entries: &[Entry]) -> Result<(), String> {
    if let Some(dir) = path.parent().filter(|d| !d.as_os_str().is_empty()) {
        fs::create_dir_all(dir).map_err(|err| format!("{}: {err}", dir.display()))?;
    }
    let text: String = entries.iter().map(|e| format!("{}\n", e.to_json())).collect();
    fs::File::create(path)
        .and_then(|mut f| f.write_all(text.as_bytes()))
        .map_err(|err| format!("{}: {err}", path.display()))
}

/// Embeds a dataset and writes it to `out`. Returns the number of embeddings written.
pub fn export_dataset(
    model: &GcniiStar,
    samples: &[Sample],
    classes: &[String],
    normalize: bool,
    out: &Path,
) -> Result<usize, String> {
    let entries: Vec<Entry> = samples
        .iter()
        .map(|s| Entry {
            path: &s.path,
            class: Some(&classes[s.label]),
            names: &s.graph.names,
            embedding: embed(model, &s.graph, normalize),
        })
        .collect();
    write_jsonl(out, &entries)?;
    Ok(entries.len())
}

#[cfg(test)]
mod tests {
    use super::super::model::tests::setup;
    use super::*;

    #[test]
    fn normalize_gives_unit_length_and_keeps_zero() {
        let v = l2_normalize(&[3.0, 4.0]);
        assert!((v.iter().map(|x| x * x).sum::<f32>() - 1.0).abs() < 1e-6);
        assert_eq!(l2_normalize(&[0.0, 0.0]), vec![0.0, 0.0]);
    }

    #[test]
    fn embedding_has_hidden_floats_and_export_writes_lines() {
        let (m, g, _) = setup();
        assert_eq!(embed(&m, &g, false).len(), m.cfg.hidden);
        let dir = std::env::temp_dir().join(format!("sargo_gcnii_emb_{}", std::process::id()));
        let out = dir.join("e.jsonl");
        let entry = Entry {
            path: "g.json",
            class: Some("a"),
            names: &g.names,
            embedding: embed(&m, &g, true),
        };
        write_jsonl(&out, &[entry]).unwrap();
        let line: serde_json::Value =
            serde_json::from_str(fs::read_to_string(&out).unwrap().trim()).unwrap();
        assert_eq!(line["class"], "a");
        assert!(line.get("names").is_none());
        assert_eq!(line["embedding"].as_array().unwrap().len(), m.cfg.hidden);
        let _ = fs::remove_dir_all(dir);
    }
}
