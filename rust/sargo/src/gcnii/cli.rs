//! The `sargo gcnii ...` command line (called from `main.rs` when the first argument is `gcnii`):
//!
//!   sargo gcnii train DIR --save FILE [--epochs N=30] [--lr X=0.01] [--batch N=4] [--seed N]
//!                     [--hidden N=16] [--depth N=16] [--num-classes N]
//!   sargo gcnii eval  DIR --load FILE
//!   sargo gcnii infer GRAPH.json|- --load FILE [--classes a,b,c]
//!
//! DIR is a dataset (see `dataset.rs`). `infer` prints one JSON document (see `train::infer`); `-` reads the
//! graph from stdin. The weights file stores the model size and node types, so eval/infer need no size flags.
//! `--num-classes` only checks the dataset has that many classes (the model gets one output per class).
//! Flags can be written `--name value` or `-name value`.

use super::dataset::load_dataset;
use super::graph::Graph;
use super::model::{DEFAULT_DEPTH, DEFAULT_HIDDEN};
use super::train::{evaluate, infer, train, TrainOptions};
use super::weights;
use std::collections::HashMap;
use std::path::Path;
use std::time::{SystemTime, UNIX_EPOCH};

pub const USAGE: &str = "usage: sargo gcnii train DIR --save FILE [--epochs N=30] [--lr X=0.01] [--batch N=4] [--seed N] [--hidden N=16] [--depth N=16] [--num-classes N]
       sargo gcnii eval DIR --load FILE
       sargo gcnii infer GRAPH.json|- --load FILE [--classes a,b,c]";

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
    Ok(())
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

fn cmd_infer(graph: &str, load: &Path, classes: Option<&String>) -> Result<(), String> {
    let model = weights::load(load)?;
    let text = if graph == "-" {
        let mut t = String::new();
        std::io::Read::read_to_string(&mut std::io::stdin(), &mut t).map_err(|err| format!("stdin: {err}"))?;
        t
    } else {
        std::fs::read_to_string(graph).map_err(|err| format!("{graph}: {err}"))?
    };
    let doc: serde_json::Value = serde_json::from_str(&text).map_err(|err| format!("{graph}: {err}"))?;
    let g = Graph::from_value(&doc).map_err(|err| format!("{graph}: {err}"))?;
    let names: Vec<String> = classes
        .map(|c| c.split(',').filter(|s| !s.is_empty()).map(String::from).collect())
        .unwrap_or_default();
    println!("{}", infer(&model, &g, &names));
    Ok(())
}
