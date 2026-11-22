//! Dataset loading: every `.json` / `.jsonl` under a dir (any depth), or one `.jsonl` file (one graph per line).
//! Each graph document carries its `"class"`; a `.json` file without it falls back to the name of its parent dir
//! (datasets laid out as `<class>/*.json`). The classes are the sorted distinct class names, the labels their indexes.

use super::graph::Graph;
use std::collections::BTreeSet;
use std::fs;
use std::path::{Path, PathBuf};

pub struct Sample {
    /// File (or `file:line`) the graph came from, for messages.
    pub path: String,
    pub graph: Graph,
    pub label: usize,
}

/// Every `.json` / `.jsonl` file under `dir` (any depth), sorted by path.
fn json_files(dir: &Path) -> Vec<PathBuf> {
    let mut entries: Vec<PathBuf> = fs::read_dir(dir)
        .map(|rd| rd.filter_map(|e| e.ok()).map(|e| e.path()).collect())
        .unwrap_or_default();
    entries.sort();
    entries
        .into_iter()
        .flat_map(|p| {
            if p.is_dir() {
                json_files(&p)
            } else if p.extension().is_some_and(|e| e == "json" || e == "jsonl") {
                vec![p]
            } else {
                Vec::new()
            }
        })
        .collect()
}

/// (name for messages, class, graph document) of one JSON text.
fn parse_example(
    name: String,
    text: &str,
    parent: Option<String>,
) -> Result<(String, String, serde_json::Value), String> {
    let v: serde_json::Value = serde_json::from_str(text).map_err(|err| format!("{name}: {err}"))?;
    let class = match v.get("class") {
        Some(serde_json::Value::String(s)) if !s.is_empty() => Ok(s.clone()),
        Some(serde_json::Value::Null) | None => parent.ok_or(format!("{name}: no \"class\" field")),
        Some(_) => Err(format!("{name}: \"class\" must be a non-empty string")),
    }?;
    Ok((name, class, v))
}

fn read_examples(path: &Path) -> Result<Vec<(String, String, serde_json::Value)>, String> {
    let files = if path.is_dir() {
        json_files(path)
    } else {
        vec![path.to_path_buf()]
    };
    let per_file = files
        .iter()
        .map(|f| -> Result<Vec<_>, String> {
            let text = fs::read_to_string(f).map_err(|err| format!("{}: {err}", f.display()))?;
            if f.extension().is_some_and(|e| e == "jsonl") {
                text.lines()
                    .enumerate()
                    .filter(|(_, l)| !l.trim().is_empty())
                    .map(|(i, line)| parse_example(format!("{}:{}", f.display(), i + 1), line, None))
                    .collect()
            } else {
                let parent = f
                    .parent()
                    .filter(|p| path.is_dir() && *p != path)
                    .and_then(|p| p.file_name())
                    .map(|n| n.to_string_lossy().into_owned());
                Ok(vec![parse_example(f.display().to_string(), &text, parent)?])
            }
        })
        .collect::<Result<Vec<_>, String>>()?;
    Ok(per_file.into_iter().flatten().collect())
}

/// The number of distinct classes of a dataset (0 if it does not exist or cannot be read).
pub fn count_classes(path: &Path) -> usize {
    read_examples(path)
        .map(|e| e.into_iter().map(|x| x.1).collect::<BTreeSet<_>>().len())
        .unwrap_or(0)
}

/// Reads a dataset: the samples (sorted by class, then path) and the sorted class names.
pub fn load_dataset(path: &Path) -> Result<(Vec<Sample>, Vec<String>), String> {
    let examples = read_examples(path)?;
    if examples.is_empty() {
        return Err(format!("no graphs found in {}", path.display()));
    }
    build_samples(examples)
}

/// The samples and classes of graph documents already in memory (a training request), each carrying its `"class"`.
/// Same result as `load_dataset`; the graphs are named `graphs[i]` in messages.
pub fn samples_from_values(docs: &[serde_json::Value]) -> Result<(Vec<Sample>, Vec<String>), String> {
    if docs.is_empty() {
        return Err("no graphs to train on".to_string());
    }
    docs.iter()
        .enumerate()
        .map(|(i, doc)| parse_example(format!("graphs[{i}]"), &doc.to_string(), None))
        .collect::<Result<Vec<_>, String>>()
        .and_then(build_samples)
}

fn build_samples(
    examples: Vec<(String, String, serde_json::Value)>,
) -> Result<(Vec<Sample>, Vec<String>), String> {
    let classes: Vec<String> = examples
        .iter()
        .map(|e| e.1.clone())
        .collect::<BTreeSet<_>>()
        .into_iter()
        .collect();
    let mut samples = examples
        .into_iter()
        .map(|(name, class, v)| {
            Graph::from_value(&v)
                .map_err(|err| format!("{name}: {err}"))
                .map(|graph| Sample {
                    path: name,
                    graph,
                    label: classes.binary_search(&class).unwrap(),
                })
        })
        .collect::<Result<Vec<_>, String>>()?;
    samples.sort_by(|a, b| (a.label, &a.path).cmp(&(b.label, &b.path)));
    Ok((samples, classes))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn loads_classes_from_documents_and_dirs() {
        let dir = std::env::temp_dir().join(format!("sargo_gcnii_ds_{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(dir.join("chain")).unwrap();
        fs::write(dir.join("chain/a.json"), r#"{"n":2,"edges":[[0,1]]}"#).unwrap();
        fs::write(
            dir.join("b.jsonl"),
            "{\"n\":3,\"edges\":[[0,1],[1,2]],\"class\":\"star\"}\n\n{\"n\":2,\"edges\":[],\"class\":\"star\"}\n",
        )
        .unwrap();
        let (samples, classes) = load_dataset(&dir).unwrap();
        assert_eq!(classes, vec!["chain", "star"]);
        assert_eq!(samples.iter().map(|s| s.label).collect::<Vec<_>>(), vec![0, 1, 1]);
        assert_eq!(count_classes(&dir), 2);
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn samples_from_documents() {
        let docs: Vec<serde_json::Value> = vec![
            serde_json::json!({"n":2,"edges":[[0,1]],"class":"b"}),
            serde_json::json!({"n":2,"edges":[],"class":"a"}),
        ];
        let (samples, classes) = samples_from_values(&docs).unwrap();
        assert_eq!(classes, vec!["a", "b"]);
        assert_eq!(samples.len(), 2);
        assert!(samples_from_values(&[serde_json::json!({"n":1,"edges":[]})]).is_err());
        assert!(samples_from_values(&[]).is_err());
    }

    #[test]
    fn jsonl_line_without_class_is_an_error() {
        let dir = std::env::temp_dir().join(format!("sargo_gcnii_ds2_{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        fs::write(dir.join("x.jsonl"), "{\"n\":1,\"edges\":[]}\n").unwrap();
        assert!(load_dataset(&dir).is_err());
        let _ = fs::remove_dir_all(dir);
    }
}
