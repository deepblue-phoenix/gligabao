//! Per-user GCNII* models: one model per user id, trained on request and served from memory.
//!
//! Layout on disk (`[gcnii] models_dir`): `<models_dir>/<hex of the user id>/model.weights` and `classes.json`. The user id is
//! hex-encoded, so any id is a safe directory name and two ids never collide.
//!
//! Training runs on a background thread (one at a time per user); `start` returns at once and `status` tells how it is going:
//! `running`, `done` (with accuracy / loss on the training data) or `failed` (with the error). A training is a classification training
//! (`start`) or a link-prediction one (`start_link`, see `link_train.rs`; it can start from the user's current model and gives it a link head). A finished model replaces the
//! user's previous one in the in-memory cache and on disk (the weights file is written to a temporary name and renamed).
//!
//! MEMORY NOTE: `UserModels` is shared as `Arc<UserModels>` between the HTTP threads and the training threads. Its two maps are
//! behind `Mutex`es held only for a lookup or an insert, never while training or reading a file. A loaded model is an
//! `Arc<UserModel>`, so inference borrows it without copying the weights and a retrain swaps the `Arc`, never mutating it.

use super::dataset::samples_from_split;
use super::link_eval::{compare, EvalOptions};
use super::link_train::{train_link, Encoder, LinkOptions};
use super::model::{GcniiStar, DEFAULT_DEPTH, DEFAULT_HIDDEN};
use super::train::{evaluate, train_with, TrainControl, TrainOptions};
use super::weights;
use serde_json::{json, Value};
use std::collections::HashMap;
use std::fs;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::{SystemTime, UNIX_EPOCH};

const WEIGHTS_FILE: &str = "model.weights";
const CLASSES_FILE: &str = "classes.json";
const MAX_USER_ID_BYTES: usize = 256;

/// A user's trained model and the names of its outputs.
pub struct UserModel {
    pub model: GcniiStar,
    pub classes: Vec<String>,
}

/// An error for the caller: an HTTP-like status and a message.
pub type UserError = (u16, String);

pub struct UserModels {
    dir: PathBuf,
    /// The loaded models with the modification time of the weights file they were loaded from: another process (or another instance of a
    /// cluster, through a restore) may replace the file, and `get` then loads it again.
    models: Mutex<HashMap<String, (Option<SystemTime>, Arc<UserModel>)>>,
    jobs: Mutex<HashMap<String, Value>>,
    /// The control of the running (or last) training of each user: its progress, and the flag that stops it.
    controls: Mutex<HashMap<String, Arc<TrainControl>>>,
}

/// The error a training that was stopped ends with (see `UserModels::cancel`).
const CANCELLED: &str = "cancelled";

fn locked<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poisoned| poisoned.into_inner())
}

fn now_unix() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// The user id as it may be used: non-empty, not absurdly long.
fn check_user_id(user_id: &str) -> Result<(), UserError> {
    match user_id {
        "" => Err((400, "user_id is required".to_string())),
        id if id.len() > MAX_USER_ID_BYTES => Err((400, format!("user_id is longer than {MAX_USER_ID_BYTES} bytes"))),
        _ => Ok(()),
    }
}

/// The directory name of a user: the hex of the id bytes.
fn dir_name(user_id: &str) -> String {
    user_id.bytes().map(|b| format!("{b:02x}")).collect()
}

/// Training options of a request body: every key is optional (`epochs`, `lr`, `batch`, `hidden`, `depth`, `seed`).
pub fn options_from_value(doc: &Value) -> Result<TrainOptions, UserError> {
    let num = |key: &str| -> Result<Option<&Value>, UserError> { Ok(doc.get(key).filter(|v| !v.is_null())) };
    let uint = |key: &str, default: u64, min: u64| -> Result<u64, UserError> {
        num(key)?.map_or(Ok(default), |v| {
            v.as_u64()
                .filter(|&n| n >= min)
                .ok_or((400, format!("{key} must be a whole number >= {min}")))
        })
    };
    let lr = num("lr")?.map_or(Ok(0.01), |v| {
        v.as_f64()
            .filter(|&x| x > 0.0 && x.is_finite())
            .ok_or((400, "lr must be a number > 0".to_string()))
    })?;
    Ok(TrainOptions {
        epochs: uint("epochs", 30, 1)? as usize,
        lr: lr as f32,
        batch: uint("batch", 4, 1)? as usize,
        hidden: uint("hidden", DEFAULT_HIDDEN as u64, 1)? as usize,
        depth: uint("depth", DEFAULT_DEPTH as u64, 1)? as usize,
        seed: uint("seed", now_unix(), 0)?,
        grad_stats: None,
        grad_map: None,
    })
}

/// The outcome of a training: the model and the numbers the job reports (accuracy and loss on the training graphs and, if there were validation
/// graphs, on those; for a link training the link metrics, see `link_eval.rs`).
type Trained = (UserModel, Value);

/// Writes the model of a user (weights through a temporary name, then the classes).
fn save_user_model(dir: &Path, model: &GcniiStar, classes: &[String]) -> Result<(), String> {
    fs::create_dir_all(dir).map_err(|err| format!("{}: {err}", dir.display()))?;
    let tmp = dir.join(format!("{WEIGHTS_FILE}.tmp"));
    weights::save(model, &tmp)?;
    fs::rename(&tmp, dir.join(WEIGHTS_FILE)).map_err(|err| format!("cannot save the weights: {err}"))?;
    fs::write(dir.join(CLASSES_FILE), json!(classes).to_string()).map_err(|err| format!("cannot save the classes: {err}"))
}

/// Trains, evaluates on the training data (and on the validation data, if any) and saves the model of one user into `dir`.
fn run_training(
    dir: &Path,
    docs: &[Value],
    validation: &[Value],
    opts: &TrainOptions,
    ctl: &TrainControl,
) -> Result<Trained, String> {
    let (samples, val_samples, classes) = samples_from_split(docs, validation)?;
    let model = train_with(&samples, &classes, opts, Some(ctl)).ok_or_else(|| CANCELLED.to_string())?;
    let (accuracy, loss) = evaluate(&model, &samples)?;
    let mut numbers = json!({"accuracy": accuracy, "loss": loss});
    if !val_samples.is_empty() {
        let (val_accuracy, val_loss) = evaluate(&model, &val_samples)?;
        numbers["val_accuracy"] = json!(val_accuracy);
        numbers["val_loss"] = json!(val_loss);
    }
    save_user_model(dir, &model, &classes)?;
    Ok((UserModel { model, classes }, numbers))
}

/// Trains the link head (see `link_train.rs`) of the model of one user, starting from `init` (the user's model) if given, evaluates both ways
/// to predict links on the training graphs (and on the validation ones) and saves the model, with its head, into `dir`.
fn run_link_training(
    dir: &Path,
    docs: &[Value],
    validation: &[Value],
    opts: &LinkOptions,
    init: Option<Arc<UserModel>>,
    ctl: &TrainControl,
) -> Result<Trained, String> {
    let (samples, val_samples, found_classes) = samples_from_split(docs, validation)?;
    let classes = init.as_ref().map_or(found_classes, |m| m.classes.clone());
    let start = init.map(|m| m.model.clone());
    let model = train_link(&samples, &classes, start, opts, Some(ctl))?.ok_or_else(|| CANCELLED.to_string())?;
    let eval = EvalOptions {
        seed: opts.seed,
        ..Default::default()
    };
    let scored = |set: &[super::dataset::Sample]| compare(&model, set, &eval).unwrap_or_else(|err| json!({"error": err}));
    let (_, last_loss) = ctl.progress();
    let mut numbers = json!({"task": "link", "link_loss": last_loss, "link_eval": scored(&samples)});
    if !val_samples.is_empty() {
        numbers["val_link_eval"] = scored(&val_samples);
    }
    save_user_model(dir, &model, &classes)?;
    Ok((UserModel { model, classes }, numbers))
}

/// The body of a link training: the classification options' keys plus `holdout` (0..1, default 0.2), `encoder` (`frozen` | `train`, default
/// `frozen` when starting from the user's model, else `train`), `class_weight` (default 0, needs `encoder` train) and `init` (bool: start
/// from the current model of the user; default true for a frozen encoder, else false). Returns the options and `init`.
pub fn link_options_from_value(doc: &Value) -> Result<(LinkOptions, bool), UserError> {
    let base = options_from_value(doc)?;
    let get = |key: &str| doc.get(key).filter(|v| !v.is_null());
    let init_flag = match get("init") {
        None => None,
        Some(v) => Some(v.as_bool().ok_or((400, "init must be true or false".to_string()))?),
    };
    let encoder = match get("encoder") {
        Some(v) => Encoder::parse(v.as_str().unwrap_or("")).map_err(|err| (400, err))?,
        None if init_flag == Some(false) => Encoder::Train,
        None if init_flag == Some(true) => Encoder::Frozen,
        None => Encoder::Frozen,
    };
    let init = init_flag.unwrap_or(encoder == Encoder::Frozen);
    let holdout = get("holdout").map_or(Ok(0.2), |v| {
        v.as_f64()
            .filter(|&x| x > 0.0 && x < 1.0)
            .ok_or((400, "holdout must be a number between 0 and 1".to_string()))
    })?;
    let class_weight = get("class_weight").map_or(Ok(0.0), |v| {
        v.as_f64()
            .filter(|&x| x >= 0.0 && x.is_finite())
            .ok_or((400, "class_weight must be a number >= 0".to_string()))
    })?;
    if encoder == Encoder::Frozen && !init {
        return Err((400, "a frozen encoder needs init (a model to start from)".to_string()));
    }
    if encoder == Encoder::Frozen && class_weight > 0.0 {
        return Err((400, "class_weight needs encoder train".to_string()));
    }
    Ok((
        LinkOptions {
            epochs: base.epochs,
            lr: base.lr,
            batch: base.batch,
            hidden: base.hidden,
            depth: base.depth,
            seed: base.seed,
            holdout: holdout as f32,
            encoder,
            class_weight: class_weight as f32,
        },
        init,
    ))
}

/// Graph documents of a link training do not need a `class` (it only matters with a class weight): a missing one becomes `_`.
fn with_default_class(docs: &[Value], needs_class: bool) -> Vec<Value> {
    docs.iter()
        .map(|d| match d {
            Value::Object(o) if !needs_class && o.get("class").map_or(true, Value::is_null) => {
                let mut o = o.clone();
                o.insert("class".to_string(), json!("_"));
                Value::Object(o)
            }
            other => other.clone(),
        })
        .collect()
}

/// When the weights file of `dir` was last written (`None` if it has none).
fn weights_mtime(dir: &Path) -> Option<SystemTime> {
    fs::metadata(dir.join(WEIGHTS_FILE)).and_then(|m| m.modified()).ok()
}

/// Reads a saved user model from `dir`; `Ok(None)` if the user has none.
fn load_from(dir: &Path) -> Result<Option<UserModel>, String> {
    let weights_path = dir.join(WEIGHTS_FILE);
    if !weights_path.is_file() {
        return Ok(None);
    }
    let model = weights::load(&weights_path)?;
    let classes = match fs::read_to_string(dir.join(CLASSES_FILE)) {
        Ok(text) => serde_json::from_str::<Vec<String>>(&text).map_err(|err| format!("{CLASSES_FILE}: {err}"))?,
        Err(_) => Vec::new(),
    };
    Ok(Some(UserModel { model, classes }))
}

impl UserModels {
    /// Uses (and creates) `dir` as the root of the per-user models.
    pub fn new(dir: &Path) -> Result<UserModels, String> {
        fs::create_dir_all(dir).map_err(|err| format!("cannot create gcnii models_dir {}: {err}", dir.display()))?;
        Ok(UserModels {
            dir: dir.to_path_buf(),
            models: Mutex::new(HashMap::new()),
            jobs: Mutex::new(HashMap::new()),
            controls: Mutex::new(HashMap::new()),
        })
    }

    fn user_dir(&self, user_id: &str) -> PathBuf {
        self.dir.join(dir_name(user_id))
    }

    /// The model of a user (from the cache, else from disk), `None` if never trained.
    pub fn get(&self, user_id: &str) -> Result<Option<Arc<UserModel>>, UserError> {
        check_user_id(user_id)?;
        let dir = self.user_dir(user_id);
        let mtime = weights_mtime(&dir);
        if let Some((loaded_mtime, found)) = locked(&self.models).get(user_id) {
            if *loaded_mtime == mtime {
                return Ok(Some(Arc::clone(found)));
            }
        }
        // not loaded yet, or the file was replaced since
        let loaded = load_from(&dir).map_err(|err| (500, err))?;
        Ok(loaded.map(|m| {
            let model = Arc::new(m);
            locked(&self.models).insert(user_id.to_string(), (mtime, Arc::clone(&model)));
            model
        }))
    }

    /// The state of the last training of a user.
    pub fn status(&self, user_id: &str) -> Result<Value, UserError> {
        check_user_id(user_id)?;
        let trained = self.user_dir(user_id).join(WEIGHTS_FILE).is_file();
        Ok(match locked(&self.jobs).get(user_id) {
            Some(job) => {
                let mut job = job.clone();
                if job["status"] == "running" {
                    if let Some(ctl) = locked(&self.controls).get(user_id) {
                        let (epoch, loss) = ctl.progress();
                        job["epoch"] = json!(epoch);
                        if epoch > 0 {
                            job["epoch_loss"] = json!(loss);
                        }
                    }
                }
                // every finished epoch's loss (also after the job ended), so a watcher that polls slower than the epochs misses none
                if let Some(ctl) = locked(&self.controls).get(user_id) {
                    job["epoch_losses"] = json!(ctl.epoch_losses());
                }
                json!({"user_id": user_id, "trained": trained, "job": job})
            }
            None => json!({"user_id": user_id, "trained": trained, "job": null}),
        })
    }

    /// Starts training the model of `user_id` on the graph documents (each with its `"class"`) in the background; `validation`
    /// (may be empty) are graphs the model is not trained on, scored after the training (`val_accuracy`, `val_loss`).
    /// The data is checked first (400 for a bad document); 409 if this user is already training.
    pub fn start(
        self: &Arc<Self>,
        user_id: &str,
        docs: &[Value],
        validation: &[Value],
        opts: TrainOptions,
    ) -> Result<Value, UserError> {
        check_user_id(user_id)?;
        let (_, _, classes) = samples_from_split(docs, validation).map_err(|err| (400, err))?;
        let job = json!({
            "status": "running",
            "task": "classify",
            "graphs": docs.len(),
            "validation_graphs": validation.len(),
            "classes": classes,
            "epochs": opts.epochs,
            "started_unix": now_unix(),
        });
        let (docs, validation) = (docs.to_vec(), validation.to_vec());
        self.launch(user_id, job, move |dir, ctl| run_training(dir, &docs, &validation, &opts, ctl))
    }

    /// Starts the link-prediction training of the model of `user_id` (see `link_train.rs`): the graphs need no `class` unless
    /// `opts.class_weight` > 0. With `from_current` it starts from the user's trained model (404 if there is none), which keeps what it learned
    /// and gets a link head; the job reports `link_eval` (both ways to predict links, on the training graphs) and `val_link_eval`.
    pub fn start_link(
        self: &Arc<Self>,
        user_id: &str,
        docs: &[Value],
        validation: &[Value],
        opts: LinkOptions,
        from_current: bool,
    ) -> Result<Value, UserError> {
        check_user_id(user_id)?;
        let needs_class = opts.class_weight > 0.0;
        let (docs, validation) = (with_default_class(docs, needs_class), with_default_class(validation, needs_class));
        let (_, _, classes) = samples_from_split(&docs, &validation).map_err(|err| (400, err))?;
        let init = if from_current {
            Some(
                self.get(user_id)?
                    .ok_or((404, format!("user {user_id} has no trained model to start the link training from")))?,
            )
        } else {
            None
        };
        let job = json!({
            "status": "running",
            "task": "link",
            "graphs": docs.len(),
            "validation_graphs": validation.len(),
            "classes": classes,
            "epochs": opts.epochs,
            "encoder": format!("{:?}", opts.encoder).to_lowercase(),
            "from_current": from_current,
            "started_unix": now_unix(),
        });
        self.launch(user_id, job, move |dir, ctl| run_link_training(dir, &docs, &validation, &opts, init, ctl))
    }

    /// Registers the job (409 if the user is already training) and runs `work` on a background thread.
    fn launch<F>(self: &Arc<Self>, user_id: &str, job: Value, work: F) -> Result<Value, UserError>
    where
        F: FnOnce(&Path, &TrainControl) -> Result<Trained, String> + Send + 'static,
    {
        {
            let mut jobs = locked(&self.jobs);
            if jobs.get(user_id).is_some_and(|j| j["status"] == "running") {
                return Err((409, format!("a training for user {user_id} is already running")));
            }
            jobs.insert(user_id.to_string(), job.clone());
        }
        let ctl = Arc::new(TrainControl::default());
        locked(&self.controls).insert(user_id.to_string(), Arc::clone(&ctl));
        let running = job.clone();
        let (this, user, dir) = (Arc::clone(self), user_id.to_string(), self.user_dir(user_id));
        std::thread::spawn(move || {
            let outcome = catch_unwind(AssertUnwindSafe(|| work(&dir, &ctl))).unwrap_or_else(|_| Err("training panicked".to_string()));
            this.finish(&user, running, outcome);
        });
        Ok(json!({"user_id": user_id, "job": job}))
    }

    /// Stops the running training of `user_id`: it ends (status `cancelled`) at its next batch, and the user's previous model, if any,
    /// is left as it was. 409 if the user is not training.
    pub fn cancel(&self, user_id: &str) -> Result<Value, UserError> {
        check_user_id(user_id)?;
        let running = locked(&self.jobs).get(user_id).is_some_and(|j| j["status"] == "running");
        match locked(&self.controls).get(user_id) {
            Some(ctl) if running => ctl.cancel.store(true, std::sync::atomic::Ordering::Relaxed),
            _ => return Err((409, format!("user {user_id} is not training"))),
        }
        self.status(user_id)
    }

    /// Records the outcome of a training: swaps in the new model and updates the job.
    fn finish(&self, user_id: &str, job: Value, outcome: Result<Trained, String>) {
        let done_at = now_unix();
        let finished = match outcome {
            Ok((model, numbers)) => {
                let mtime = weights_mtime(&self.user_dir(user_id));
                locked(&self.models).insert(user_id.to_string(), (mtime, Arc::new(model)));
                let mut done = json!({"status": "done", "finished_unix": done_at});
                if let (Value::Object(base), Value::Object(extra)) = (&mut done, numbers) {
                    base.extend(extra);
                }
                done
            }
            Err(err) if err == CANCELLED => json!({"status": "cancelled", "finished_unix": done_at}),
            Err(err) => {
                eprintln!("gcnii training of user {user_id} failed: {err}");
                json!({"status": "failed", "error": err, "finished_unix": done_at})
            }
        };
        let merged = match (job, finished) {
            (Value::Object(mut base), Value::Object(extra)) => {
                base.extend(extra);
                Value::Object(base)
            }
            (_, extra) => extra,
        };
        locked(&self.jobs).insert(user_id.to_string(), merged);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::{Duration, Instant};

    fn docs() -> Vec<Value> {
        (3..8)
            .flat_map(|n| {
                let path: Vec<Value> = (0..n - 1).map(|i| json!([i, i + 1])).collect();
                let clique: Vec<Value> = (0..n).flat_map(|i| (i + 1..n).map(move |j| json!([i, j]))).collect();
                vec![
                    json!({"n": n, "edges": path, "class": "path"}),
                    json!({"n": n, "edges": clique, "class": "clique"}),
                ]
            })
            .collect()
    }

    fn wait_done(models: &UserModels, user: &str) -> Value {
        let start = Instant::now();
        loop {
            let status = models.status(user).unwrap();
            if status["job"]["status"] != "running" || start.elapsed() > Duration::from_secs(60) {
                return status;
            }
            std::thread::sleep(Duration::from_millis(20));
        }
    }

    #[test]
    fn cancel_stops_a_training_and_keeps_the_old_model() {
        let dir = std::env::temp_dir().join(format!("sargo_gcnii_cancel_{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let models = Arc::new(UserModels::new(&dir).unwrap());
        assert_eq!(models.cancel("alice").unwrap_err().0, 409, "nothing to cancel");
        let opts = options_from_value(&json!({"epochs": 100000, "hidden": 4, "depth": 2, "seed": 1})).unwrap();
        models.start("alice", &docs(), &[], opts).unwrap();
        models.cancel("alice").unwrap();
        let status = wait_done(&models, "alice");
        assert_eq!(status["job"]["status"], "cancelled", "{status}");
        assert_eq!(status["trained"], false, "a cancelled training saves nothing");
        assert_eq!(models.cancel("alice").unwrap_err().0, 409, "it is over");
    }

    #[test]
    fn trains_per_user_and_reloads_from_disk() {
        let dir = std::env::temp_dir().join(format!("sargo_gcnii_users_{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let models = Arc::new(UserModels::new(&dir).unwrap());
        assert!(models.get("alice").unwrap().is_none());
        let opts = options_from_value(&json!({"epochs": 5, "hidden": 4, "depth": 2, "seed": 1})).unwrap();
        models.start("alice", &docs(), &[], opts).unwrap();
        let status = wait_done(&models, "alice");
        assert_eq!(status["job"]["status"], "done", "{status}");
        assert_eq!(status["trained"], true);
        assert!(models.get("alice").unwrap().is_some());
        assert!(models.get("bob").unwrap().is_none(), "the model of alice is not bob's");
        // a file written by someone else (a restore from the other instance of a cluster) replaces the loaded model
        let before = models.get("alice").unwrap().unwrap();
        std::thread::sleep(Duration::from_millis(20));
        let file = models.user_dir("alice").join(WEIGHTS_FILE);
        let mut bytes = fs::read(&file).unwrap();
        let last = bytes.len() - 4;
        bytes[last..].copy_from_slice(&0.5f32.to_le_bytes());
        fs::write(&file, &bytes).unwrap();
        let after = models.get("alice").unwrap().unwrap();
        assert!(!Arc::ptr_eq(&before, &after), "the replaced file was not reloaded");
        assert_eq!(*after.model.params.last().unwrap().last().unwrap(), 0.5);
        assert!(Arc::ptr_eq(&after, &models.get("alice").unwrap().unwrap()), "an unchanged file is loaded again");
        // a fresh process (empty cache) finds the saved model and its classes
        let reloaded = UserModels::new(&dir).unwrap().get("alice").unwrap().unwrap();
        assert_eq!(reloaded.classes, vec!["clique", "path"]);
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn scores_the_validation_graphs() {
        let dir = std::env::temp_dir().join(format!("sargo_gcnii_users3_{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let models = Arc::new(UserModels::new(&dir).unwrap());
        let opts = options_from_value(&json!({"epochs": 3, "hidden": 4, "depth": 2, "seed": 1})).unwrap();
        let all = docs();
        let (train, val) = all.split_at(8);
        let job = models.start("carol", train, val, opts).unwrap();
        assert_eq!(job["job"]["validation_graphs"], 2);
        let status = wait_done(&models, "carol");
        assert_eq!(status["job"]["status"], "done", "{status}");
        assert!(status["job"]["val_accuracy"].is_number() && status["job"]["val_loss"].is_number(), "{status}");
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn link_training_starts_from_the_classifier_and_keeps_it() {
        let dir = std::env::temp_dir().join(format!("sargo_gcnii_users4_{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let models = Arc::new(UserModels::new(&dir).unwrap());
        let link = |extra: Value| {
            let mut body = json!({"epochs": 4, "hidden": 4, "depth": 2, "seed": 1});
            body.as_object_mut().unwrap().extend(extra.as_object().unwrap().clone());
            link_options_from_value(&body).unwrap()
        };
        let (opts, from_current) = link(json!({}));
        assert!(from_current, "frozen by default from the current model");
        assert_eq!(models.start_link("dave", &docs(), &[], opts, from_current).unwrap_err().0, 404, "nothing to start from");
        let opts = options_from_value(&json!({"epochs": 3, "hidden": 4, "depth": 2, "seed": 1})).unwrap();
        models.start("dave", &docs(), &[], opts).unwrap();
        assert_eq!(wait_done(&models, "dave")["job"]["status"], "done");
        let before = models.get("dave").unwrap().unwrap();
        assert!(before.model.link.is_none());
        let (opts, from_current) = link(json!({}));
        let unlabeled: Vec<Value> = docs().into_iter().map(|mut d| { d.as_object_mut().unwrap().remove("class"); d }).collect();
        models.start_link("dave", &unlabeled, &[], opts, from_current).unwrap();
        let status = wait_done(&models, "dave");
        assert_eq!(status["job"]["status"], "done", "{status}");
        assert_eq!(status["job"]["task"], "link");
        assert!(status["job"]["link_eval"]["head"]["auc"].is_number() && status["job"]["link_eval"]["cosine"]["auc"].is_number(), "{status}");
        let after = models.get("dave").unwrap().unwrap();
        assert!(after.model.link.is_some());
        assert_eq!(after.model.params, before.model.params, "the classifier is unchanged");
        assert_eq!(after.classes, before.classes);
        // from scratch needs the encoder trained
        assert!(link_options_from_value(&json!({"init": false, "encoder": "frozen"})).is_err());
        assert!(link_options_from_value(&json!({"encoder": "frozen", "class_weight": 1})).is_err());
        let (_, from_current) = link(json!({"init": false}));
        assert!(!from_current);
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn rejects_bad_input() {
        let dir = std::env::temp_dir().join(format!("sargo_gcnii_users2_{}", std::process::id()));
        let models = Arc::new(UserModels::new(&dir).unwrap());
        let opts = || options_from_value(&json!({})).unwrap();
        assert_eq!(models.start("", &docs(), &[], opts()).unwrap_err().0, 400);
        assert_eq!(models.start("u", &[], &[], opts()).unwrap_err().0, 400);
        assert_eq!(models.start("u", &[json!({"n": 1, "edges": []})], &[], opts()).unwrap_err().0, 400);
        assert!(options_from_value(&json!({"epochs": 0})).is_err());
        let _ = fs::remove_dir_all(dir);
    }
}
