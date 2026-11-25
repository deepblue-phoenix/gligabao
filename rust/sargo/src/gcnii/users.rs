//! Per-user GCNII* models: one model per user id, trained on request and served from memory.
//!
//! Layout on disk (`[gcnii] models_dir`): `<models_dir>/<hex of the user id>/model.weights` and `classes.json`. The user id is
//! hex-encoded, so any id is a safe directory name and two ids never collide.
//!
//! Training runs on a background thread (one at a time per user); `start` returns at once and `status` tells how it is going:
//! `running`, `done` (with accuracy / loss on the training data) or `failed` (with the error). A finished model replaces the
//! user's previous one in the in-memory cache and on disk (the weights file is written to a temporary name and renamed).
//!
//! MEMORY NOTE: `UserModels` is shared as `Arc<UserModels>` between the HTTP threads and the training threads. Its two maps are
//! behind `Mutex`es held only for a lookup or an insert, never while training or reading a file. A loaded model is an
//! `Arc<UserModel>`, so inference borrows it without copying the weights and a retrain swaps the `Arc`, never mutating it.

use super::dataset::samples_from_split;
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

/// The outcome of a training: the model, (accuracy, loss) on the training graphs and, if there were validation graphs, on those.
type Trained = (UserModel, (f32, f32), Option<(f32, f32)>);

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
    let scores = evaluate(&model, &samples)?;
    let val_scores = if val_samples.is_empty() { None } else { Some(evaluate(&model, &val_samples)?) };
    fs::create_dir_all(dir).map_err(|err| format!("{}: {err}", dir.display()))?;
    let tmp = dir.join(format!("{WEIGHTS_FILE}.tmp"));
    weights::save(&model, &tmp)?;
    fs::rename(&tmp, dir.join(WEIGHTS_FILE)).map_err(|err| format!("cannot save the weights: {err}"))?;
    fs::write(dir.join(CLASSES_FILE), json!(classes).to_string())
        .map_err(|err| format!("cannot save the classes: {err}"))?;
    Ok((UserModel { model, classes }, scores, val_scores))
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
            "graphs": docs.len(),
            "validation_graphs": validation.len(),
            "classes": classes,
            "epochs": opts.epochs,
            "started_unix": now_unix(),
        });
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
        let (this, user, docs, validation, dir) =
            (Arc::clone(self), user_id.to_string(), docs.to_vec(), validation.to_vec(), self.user_dir(user_id));
        std::thread::spawn(move || {
            let outcome = catch_unwind(AssertUnwindSafe(|| run_training(&dir, &docs, &validation, &opts, &ctl)))
                .unwrap_or_else(|_| Err("training panicked".to_string()));
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
            Ok((model, (accuracy, loss), val_scores)) => {
                let mtime = weights_mtime(&self.user_dir(user_id));
                locked(&self.models).insert(user_id.to_string(), (mtime, Arc::new(model)));
                let mut done = json!({"status": "done", "accuracy": accuracy, "loss": loss, "finished_unix": done_at});
                if let Some((val_accuracy, val_loss)) = val_scores {
                    done["val_accuracy"] = json!(val_accuracy);
                    done["val_loss"] = json!(val_loss);
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
