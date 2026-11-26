//! The `sargo gcnii ...` command line (called from `main.rs` when the first argument is `gcnii`):
//!
//!   sargo gcnii train DIR --save FILE [--epochs N=30] [--lr X=0.01] [--batch N=4] [--seed N]
//!                     [--hidden N=16] [--depth N=16] [--num-classes N]
//!   sargo gcnii eval  DIR --load FILE
//!   sargo gcnii infer GRAPH.json|- --load FILE [--classes a,b,c]
//!   sargo gcnii embed DIR|GRAPH.json|- --load FILE --out FILE [--normalize]
//!   sargo gcnii link  GRAPH.json|- --load FILE [--type T] [--name N] [--neighbors 0,3] [--top-k K]
//!
//!   sargo gcnii train-link DIR --save FILE [--init FILE] [--encoder frozen|train] [--class-weight X=0] [--holdout X=0.2]
//!                          [--epochs N=30] [--lr X=0.01] [--batch N=4] [--seed N] [--hidden N=16] [--depth N=16]
//!   sargo gcnii eval-link DIR --load FILE [--nodes N=5] [--reveal X=0.5] [--seed N]
//!
//! `train-link` trains the link-prediction head (`link_train.rs`), next to the classification training: from a model trained with `train`
//! (`--init`, the encoder is then `frozen` by default and the model keeps classifying as before) or from scratch (`--encoder train`, with
//! `--class-weight` > 0 to train the classification at the same time). `eval-link` tests both ways to predict links, the cosine of the
//! embeddings and the head, on the same nodes held out of the graphs (`link_eval.rs`). `link --method cosine|head` picks the way.
//!
//! `link` is link prediction: it prints the scores of a new node against every node of the graph (`link.rs`).
//!
//! `train` also takes `--grad-stats K` (@grad lines, a gradient histogram per K steps), `--grad-map K` (@gradmap
//! lines, the per-element mean |gradient|; see `metrics.rs`) and `--export-embeddings FILE [--normalize]`
//! (embeds the training dataset after saving the weights). Off by default; off, nothing extra is computed.
//! `embed` writes JSON lines `{"path", "class"?, "names"?, "embedding"}`, the embedding being the pooled vector
//! before the head (`hidden` floats); `--normalize` scales it to unit length.
//!
//! DIR is a dataset (see `dataset.rs`). `infer` prints one JSON document (see `train::infer`); `-` reads the
//! graph from stdin. The weights file stores the model size and node types, so eval/infer need no size flags.
//! `--num-classes` only checks the dataset has that many classes (the model gets one output per class).
//! Flags can be written `--name value` or `-name value`.

use super::dataset::load_dataset;
use super::embed::{embed, export_dataset, write_jsonl, Entry};
use super::graph::Graph;
use super::link::{predict, Method, NewNode};
use super::link_eval::{compare, EvalOptions};
use super::link_train::{train_link, Encoder, LinkOptions};
use super::model::{DEFAULT_DEPTH, DEFAULT_HIDDEN};
use super::train::{evaluate, infer, train, TrainOptions};
use super::weights;
use std::collections::HashMap;
use std::path::Path;
use std::time::{SystemTime, UNIX_EPOCH};

pub const USAGE: &str = "usage: sargo gcnii train DIR --save FILE [--epochs N=30] [--lr X=0.01] [--batch N=4] [--seed N] [--hidden N=16] [--depth N=16] [--num-classes N]
       sargo gcnii eval DIR --load FILE
       sargo gcnii infer GRAPH.json|- --load FILE [--classes a,b,c]
       sargo gcnii embed DIR|GRAPH.json|- --load FILE --out FILE [--normalize]
       sargo gcnii link GRAPH.json|- --load FILE [--type T] [--name N] [--neighbors 0,3] [--top-k K] [--method cosine|head]
       sargo gcnii train-link DIR --save FILE [--init FILE] [--encoder frozen|train] [--class-weight X=0] [--holdout X=0.2] [--epochs N] [--lr X] [--batch N] [--seed N] [--hidden N] [--depth N]
       sargo gcnii eval-link DIR --load FILE [--nodes N=5] [--reveal X=0.5] [--seed N]
       (train also: [--grad-stats K] [--grad-map K] [--export-embeddings FILE] [--normalize])";

/// `args`: everything after `gcnii`. Returns the process exit code.
pub fn run(args: &[String]) -> i32 {
    match dispatch(args) {
        Ok(()) => 0,
        Err(message) => {
            eprintln!("error: {message}");
            1
        }
    }
}

/// (positional arguments, flags).
fn parse_args(args: &[String]) -> Result<(Vec<String>, HashMap<String, String>), String> {
    let mut positional = Vec::new();
    let mut flags = HashMap::new();
    let mut it = args.iter().peekable();
    while let Some(arg) = it.next() {
        match arg.strip_prefix("--").or_else(|| arg.strip_prefix('-')).filter(|k| !k.is_empty()) {
            Some("normalize") => {
                flags.insert("normalize".to_string(), "true".to_string());
            }
            Some(key) => {
                let value = it.next().ok_or(format!("flag {arg} needs a value"))?;
                flags.insert(key.to_string(), value.clone());
            }
            None => positional.push(arg.clone()),
        }
    }
    Ok((positional, flags))
}

fn flag_num<T: std::str::FromStr>(flags: &HashMap<String, String>, key: &str, default: T) -> Result<T, String> {
    flags.get(key).map_or(Ok(default), |v| {
        v.parse().map_err(|_| format!("--{key}: bad number {v:?}"))
    })
}

/// An optional whole number >= 1 (`None` = flag not given).
fn flag_opt(flags: &HashMap<String, String>, key: &str) -> Result<Option<usize>, String> {
    flags.get(key).map_or(Ok(None), |v| {
        v.parse::<usize>()
            .ok()
            .filter(|&n| n >= 1)
            .map(Some)
            .ok_or(format!("--{key}: must be a whole number >= 1, got {v:?}"))
    })
}

/// `--normalize` is a switch without a value.
fn flag_switch(flags: &HashMap<String, String>, key: &str) -> bool {
    flags.get(key).is_some_and(|v| v != "false" && v != "0")
}

fn dispatch(args: &[String]) -> Result<(), String> {
    let (cmd, rest) = match args.split_first() {
        Some((cmd, rest)) => (cmd.as_str(), rest),
        None => return Err(format!("missing command\n{USAGE}")),
    };
    let (positional, flags) = parse_args(rest)?;
    let input = positional.first().ok_or(format!("{cmd}: missing DIR/GRAPH argument\n{USAGE}"))?;
    let need = |key: &str| flags.get(key).ok_or(format!("--{key} is needed\n{USAGE}"));
    match cmd {
        "train" => cmd_train(input, Path::new(need("save")?), &flags),
        "eval" => cmd_eval(input, Path::new(need("load")?)),
        "infer" => cmd_infer(input, Path::new(need("load")?), flags.get("classes")),
        "embed" => cmd_embed(input, Path::new(need("load")?), Path::new(need("out")?), flag_switch(&flags, "normalize")),
        "train-link" => cmd_train_link(input, Path::new(need("save")?), &flags),
        "eval-link" => cmd_eval_link(input, Path::new(need("load")?), &flags),
        "link" => cmd_link(input, Path::new(need("load")?), &flags),
        _ => Err(format!("unknown command {cmd:?}\n{USAGE}")),
    }
}

fn cmd_train(dir: &str, save: &Path, flags: &HashMap<String, String>) -> Result<(), String> {
    let seed = flag_num(
        flags,
        "seed",
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_nanos() as u64)
            .unwrap_or(1),
    )?;
    let opts = TrainOptions {
        epochs: flag_num(flags, "epochs", 30)?,
        lr: flag_num(flags, "lr", 0.01)?,
        batch: flag_num::<usize>(flags, "batch", 4)?.max(1),
        hidden: flag_num(flags, "hidden", DEFAULT_HIDDEN)?,
        depth: flag_num(flags, "depth", DEFAULT_DEPTH)?,
        seed,
        grad_stats: flag_opt(flags, "grad-stats")?,
        grad_map: flag_opt(flags, "grad-map")?,
    };
    if opts.hidden < 1 || opts.depth < 1 {
        return Err("--hidden and --depth must be >= 1".to_string());
    }
    let (samples, classes) = load_dataset(Path::new(dir))?;
    if let Some(want) = flags.get("num-classes") {
        let want: usize = want.parse().map_err(|_| format!("--num-classes: bad number {want:?}"))?;
        if want != classes.len() {
            return Err(format!(
                "{} classes {classes:?} in {dir}, --num-classes is {want}",
                classes.len()
            ));
        }
    }
    println!("training: {} graphs, {} classes {:?}", samples.len(), classes.len(), classes);
    let model = train(&samples, &classes, &opts);
    weights::save(&model, save)?;
    println!("saved weights to {}", save.display());
    match flags.get("export-embeddings") {
        Some(out) => {
            let n = export_dataset(&model, &samples, &classes, flag_switch(flags, "normalize"), Path::new(out))?;
            println!("exported {n} embeddings (dim {}) to {out}", model.cfg.hidden);
            Ok(())
        }
        None => Ok(()),
    }
}

fn cmd_eval(dir: &str, load: &Path) -> Result<(), String> {
    let model = weights::load(load)?;
    let (samples, classes) = load_dataset(Path::new(dir))?;
    let (acc, loss) = evaluate(&model, &samples)?;
    println!(
        "eval: {} graphs, {} classes {:?}: accuracy {:.1}%, avg loss {:.4}",
        samples.len(),
        classes.len(),
        classes,
        100.0 * acc,
        loss
    );
    Ok(())
}

/// `embed DIR|GRAPH.json`: a dataset dir (one line per graph, with its class) or one graph file.
fn cmd_embed(input: &str, load: &Path, out: &Path, normalize: bool) -> Result<(), String> {
    let model = weights::load(load)?;
    let n = if Path::new(input).is_dir() {
        let (samples, classes) = load_dataset(Path::new(input))?;
        export_dataset(&model, &samples, &classes, normalize, out)?
    } else {
        let g = read_graph(input)?;
        let entry = Entry {
            path: input,
            class: None,
            names: &g.names,
            embedding: embed(&model, &g, normalize),
        };
        write_jsonl(out, &[entry])?;
        1
    };
    println!("exported {n} embeddings (dim {}) to {}", model.cfg.hidden, out.display());
    Ok(())
}

/// Reads a graph document from a file, or from stdin for `-`.
fn read_graph(graph: &str) -> Result<Graph, String> {
    let text = if graph == "-" {
        let mut t = String::new();
        std::io::Read::read_to_string(&mut std::io::stdin(), &mut t).map_err(|err| format!("stdin: {err}"))?;
        t
    } else {
        std::fs::read_to_string(graph).map_err(|err| format!("{graph}: {err}"))?
    };
    let doc: serde_json::Value = serde_json::from_str(&text).map_err(|err| format!("{graph}: {err}"))?;
    Graph::from_value(&doc).map_err(|err| format!("{graph}: {err}"))
}

fn cmd_infer(graph: &str, load: &Path, classes: Option<&String>) -> Result<(), String> {
    let model = weights::load(load)?;
    let g = read_graph(graph)?;
    let names: Vec<String> = classes
        .map(|c| c.split(',').filter(|s| !s.is_empty()).map(String::from).collect())
        .unwrap_or_default();
    println!("{}", infer(&model, &g, &names));
    Ok(())
}

fn cmd_link(graph: &str, load: &Path, flags: &HashMap<String, String>) -> Result<(), String> {
    let model = weights::load(load)?;
    let g = read_graph(graph)?;
    let method = flags.get("method").map(|m| Method::parse(m)).transpose()?;
    let neighbors = flags
        .get("neighbors")
        .map(|v| {
            v.split(',')
                .filter(|s| !s.is_empty())
                .map(|s| s.trim().parse::<usize>().map_err(|_| format!("--neighbors: bad node index {s:?}")))
                .collect::<Result<Vec<_>, _>>()
        })
        .transpose()?
        .unwrap_or_default();
    let node = NewNode {
        name: flags.get("name").cloned().unwrap_or_default(),
        node_type: flags.get("type").cloned().unwrap_or_default(),
        neighbors,
    };
    println!("{}", predict(&model, &g, &node, flag_opt(flags, "top-k")?, method)?);
    Ok(())
}

fn cmd_train_link(dir: &str, save: &Path, flags: &HashMap<String, String>) -> Result<(), String> {
    let init = flags.get("init").map(|p| weights::load(Path::new(p))).transpose()?;
    let encoder = match flags.get("encoder") {
        Some(e) => Encoder::parse(e)?,
        None if init.is_some() => Encoder::Frozen,
        None => Encoder::Train,
    };
    let opts = LinkOptions {
        epochs: flag_num(flags, "epochs", 30)?,
        lr: flag_num(flags, "lr", 0.01)?,
        batch: flag_num::<usize>(flags, "batch", 4)?.max(1),
        hidden: flag_num(flags, "hidden", DEFAULT_HIDDEN)?,
        depth: flag_num(flags, "depth", DEFAULT_DEPTH)?,
        seed: flag_num(flags, "seed", SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_nanos() as u64).unwrap_or(1))?,
        holdout: flag_num(flags, "holdout", 0.2)?,
        encoder,
        class_weight: flag_num(flags, "class-weight", 0.0)?,
    };
    if !(opts.holdout > 0.0 && opts.holdout < 1.0) {
        return Err("--holdout must be between 0 and 1".to_string());
    }
    let (samples, classes) = load_dataset(Path::new(dir))?;
    println!("link training: {} graphs, {} classes {:?}", samples.len(), classes.len(), classes);
    let model = train_link(&samples, &classes, init, &opts, None)?.ok_or("cancelled")?;
    weights::save(&model, save)?;
    println!("saved weights (with the link head) to {}", save.display());
    Ok(())
}

fn cmd_eval_link(dir: &str, load: &Path, flags: &HashMap<String, String>) -> Result<(), String> {
    let model = weights::load(load)?;
    let (samples, _) = load_dataset(Path::new(dir))?;
    let defaults = EvalOptions::default();
    let opts = EvalOptions {
        nodes: flag_num(flags, "nodes", defaults.nodes)?,
        reveal: flag_num(flags, "reveal", defaults.reveal)?,
        seed: flag_num(flags, "seed", defaults.seed)?,
    };
    println!("{}", compare(&model, &samples, &opts)?);
    Ok(())
}
