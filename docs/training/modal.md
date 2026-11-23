# Modal app lifecycle

`ml_sidecar/backends/modal_backend.py`, `ml_sidecar/backends/actions.py`, `ml_sidecar/ai_train/providers/modal/deploy.py`, `ml_sidecar/ai_train/providers/modal/stop.py`, `ml_sidecar/ai_train/providers/modal/list_volumes.py`, `internal/api/admin/ml_sidecar.go`, `admin_ui/src/actions.ts`

The training code runs on [Modal](https://modal.com) as an app (`gligabao-train` by default). The [ml_sidecar](ml_sidecar.md) is what talks to Modal, and since gligabao 0.34.0 it also manages the app's **lifecycle**: you can see whether the app is deployed, deploy and redeploy it, watch the build live, cancel it, stop the app, and list the volumes it uses, all from the **training** view of the admin UI or over HTTP. None of this needs the `modal` CLI, a login or a `~/.modal.toml`: the ML sidecar uses the Modal SDK with the tokens in its environment. The CLI commands in [Running on Modal](ml_sidecar.md#remote-training-bootstrap) still work and do the same thing; the ML sidecar is the way to do it from the admin panel, or on a machine that has no CLI (the ML sidecar container).

The Go server stays out of it: it only passes the admin UI's requests on to the ML sidecar (see [the API](#the-api)).

## What you need

- The ML sidecar running with `MODAL_TOKEN_ID` and `MODAL_TOKEN_SECRET` (create them with `modal token new`; see the [ML sidecar's configuration](ml_sidecar.md#configuration)). The same variables are what the deploy, stop and volume scripts use: the ML sidecar hands them to the subprocesses it starts.
- `ML_SIDECAR_APP_NAME` set the same for the ML sidecar and the app (the default `gligabao-train` is fine). The ML sidecar passes its value to the scripts, so a deploy from the admin UI always deploys the app the ML sidecar looks up.
- The server running with the [admin UI](../../README.md) and a reachable ML sidecar (`ml_sidecar_url`).
- For running research agents in Modal (the `modal` [runner](../agents/research.md#where-the-agent-runs-the-runner)), the Linux build of the agent binary (`./run_build.sh` makes `build/gligabao_agent_linux_amd64`; the ML sidecar image has it) and a Modal secret with `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` named by `ML_SIDECAR_AGENT_SECRET`. The deploy bakes the binary into the app, so deploy again after a new one; without it the app has no agent. See [the research agent in Modal](ml_sidecar.md#the-research-agent-in-modal).
- For fine-tuning, a Modal secret with `HF_TOKEN` named by `ML_SIDECAR_HF_SECRET`, created in Modal beforehand (see [Running on Modal](ml_sidecar.md#remote-training-bootstrap)). A deploy fails, and says so in its log, if the secret the app names does not exist.

## Where the state lives

- **In Modal:** the deployed app, its images, its volumes. This is the state that matters: it survives restarts of the ML sidecar, of its container, and of the machine, and `modal` on another computer sees the same app.
- **On the ML sidecar's disk:** nothing. With the tokens in the environment the ML sidecar never reads or writes `~/.modal.toml` (which the CLI creates with `modal token set`), so a container with no home directory or a read-only filesystem works.
- **In the ML sidecar's memory:** the state and log of the last run of each action (below). An ML sidecar restart forgets them and ends a deploy that is running; the next status check simply shows the app as deployed or not.

## What the admin UI shows

In the **training** view, the *Backends* list shows, for the `modal` backend:

| Badge | Meaning |
|---|---|
| `healthy` / `unhealthy` | Modal answers with the ML sidecar's tokens. `unhealthy` has the reason as a tooltip (for example `Token not found`, a network error, or a timeout after 10 seconds). |
| `app deployed` | The app's web function (`serve_api`) is found in Modal: training jobs can be started. |
| `app not deployed` | Modal answers but has no such app: deploy it. |
| `deployment unknown` | The lookup did not give a clear answer. |
| `deploy: <stage>…` | A deploy is running (the stage is its current step). |
| `last deploy failed` / `cancelled` | The last run of the action ended that way; **View log** shows why. |

Below it, the **apps list** (since 0.35.0): the active Modal apps of this system, one row each with its state (`deployed`, `ephemeral`, `initializing`, or `not deployed` for the configured app when it is not up), its running tasks, and the buttons **Redeploy** (**Deploy** when not deployed), **Last log** (the log of the last action run on that app; **View log** while it runs), **Stop** and **Volumes**. "Of this system" means the app named `ML_SIDECAR_APP_NAME` and apps named `ML_SIDECAR_APP_NAME-<suffix>` (for example a `-dev` copy); other apps in the Modal workspace are neither shown nor touchable. Deploy and Stop act on that row's app, and the log of one app is kept separately from the others'. The list comes from Modal (`providers/modal/list_apps.py`, the equivalent of `modal app list`) and takes a few seconds, so the previous list is shown meanwhile.

The buttons described below apply to each app row: **Deploy** (**Redeploy** once the app is deployed), **Stop** (red; only while the app is deployed), **Volumes**, and **View log** / **Last log** once an action has run. The list refreshes every 5 seconds while an action runs, and when you press *Refresh*.

## Deploy

**Deploy** asks for a confirmation, starts the run and opens the live log. What happens:

1. The ML sidecar starts `ml_sidecar/ai_train/providers/modal/deploy.py` (as `python -m providers.modal.deploy`) in a subprocess, with `ml_sidecar/ai_train/` as its working directory.
2. It imports `app.py` (the app definition) and calls the Modal SDK's `app.deploy()`, with Modal's build output switched on. This is what `modal deploy app.py` does.
3. Modal uploads the `ai_train/` directory (mounted into the containers at `/pkg`), builds the images that are not cached, creates the app's functions and the web endpoint, and creates any of the volumes that do not exist yet.
4. The script exits with `0` on success; the run is then `succeeded` and the badge turns to `app deployed`. Any error ends the script with a traceback and a final `Deploy failed: <error>` line in the log, and the run is `failed`.

The first deploy builds the images (the Unsloth training image for both model sizes, and the lighter inference image without Unsloth), which takes several minutes; later deploys reuse Modal's image cache, and because the code is mounted last, a code change does not rebuild the dependency layers. Redeploying updates the app in place under the same name and web URL.

### Stages

The log dialog shows four stages as chips: *starting* (the process runs), *building images*, *creating functions*, *deployed*. The ML sidecar reads them from Modal's output (the lines about building images and creating objects) and they only move forward. They are a guide, not a protocol: if Modal changes its wording a stage may be skipped, so the log below is the source of truth.

## The live log

The dialog (`admin_ui/src/actions.ts`) has the state badge, the stage chips, the elapsed time, and the output of the command, one line each with its time since the start (`+12.3s`). Lines with `error`, `failed`, `traceback` or `exception` are red. **Follow** keeps the view at the end; scrolling up turns it off, ticking it scrolls down again. The dialog keeps the last 5000 lines.

Closing the dialog does not stop the run: it only stops watching. **View log** reopens it, also after a page reload, because the log lives in the ML sidecar. Behind it is a Server-Sent Events stream, so it is live and needs no polling:

- `log` events carry `lines`, a list of `{i, t, line}` (`i` the line's index, `t` its time in Unix seconds), with the SSE `id` of the last one.
- `state` events carry the state of the run (see [the API](#the-api)).
- The stream ends after the run is over. A client that loses the connection and reconnects gets the log again from the start (or `?from=<i>`) and skips the indices it already has.
- The output of the command is stdout and stderr merged; `\r` (progress bars) and `\n` both end a line, and colour codes are removed.

## Cancel

While a run is going the dialog has a **Cancel run** button. The ML sidecar sends the command `SIGINT` (so the SDK can clean up; the traceback of the interruption shows in the log) and kills it if it is still there 10 seconds later. The run ends as `cancelled` and can be started again. Cancelling before the deploy has finished normally leaves the previous deployment in place. What Modal had already built or cached on its side is not rolled back.

## Stop

**Stop** asks for a confirmation that names the consequences, then runs `ml_sidecar/ai_train/providers/modal/stop.py`, which does what `modal app stop` does: it looks the app up by name and tells Modal to stop it.

> **Warning:** Stopping the app terminates its running containers, **including training jobs and inference that are running**, and takes the web API offline: training and inference calls through the ML sidecar fail until you deploy again. The volumes (datasets, adapters, the Hugging Face cache) and the app's history are kept, and a stopped job's row stays in the database, where it is marked `failed` after the usual timeout (see [Not tracked yet](ml_sidecar.md#remote-training-bootstrap)).

The Stop button is not offered while the app is not deployed, and it never runs at the same time as a deploy (`409`; cancel the deploy first). The SDK has no public `App.stop` yet, so the script makes the same two internal calls as the CLI (look up the app id by name, `AppStop`); a future `modal` 1.x release could change them, which would show as a failed `stop` with the error in the log. Starting the app again is a deploy.

## Warm inference containers

An inference request (`POST /infer/query`) runs in a GPU container of the `infer_4b` / `infer_12b` function. Until 0.39.0 every request loaded the base model and the adapter again (about the whole latency of a request); since 0.39.0 the container keeps the loaded model in memory and **stays up for `ML_SIDECAR_INFER_IDLE_TIMEOUT_S` (default 60 s = 1 minute) after its last request**, so the requests that follow skip the load. When no request arrives within that time Modal shuts it down by itself; the next request starts a container and loads the model again. The answer of `/infer/query` says which it was: `cold_start` and `load_seconds`.

- One model per container: a request for another trained model (or size) replaces the loaded one. A model retrained in place (the legacy `finetuned_model_<size>`) is noticed by its file time and loaded again.
- Several concurrent requests are handled one at a time per container, as before; Modal starts more containers when requests queue up (each loads the model, and each is billed while it is up).
- The timeout is part of the function's definition: **Redeploy** the app after changing `ML_SIDECAR_INFER_IDLE_TIMEOUT_S` (or after upgrading to this version).
- The admin UI's app row shows a badge `N inference container(s) warm` (hover: function, model, requests, last use) and a **Stop inference** button that ends them at once (a request being answered fails; the app, the web API and training jobs are not touched). It runs `ml_sidecar/ai_train/providers/modal/inference_containers.py`; the action is `stop_inference`, next to `deploy` and `stop`, and the `apps` listing has an `inference` list for every app.
- How it knows them: Modal does not say which function a container runs, so each inference container records itself (task id, model, requests, last use) in a Modal Dict named `<app name>-inference`, and the listing only trusts a record while Modal still lists that task. A container that is gone leaves a record that is removed at the next listing.

### Cold and warm starts

A request is **cold** when no container of the function holds the requested model: the first request after a deploy, the first one after the container was idle for `ML_SIDECAR_INFER_IDLE_TIMEOUT_S`, one for another trained model or size than the one loaded, and one that Modal sends to an extra container because the others are busy. It is **warm** when a container that is already up holds that model: it only generates. This is the GPU *inference* container (`infer_4b` / `infer_12b`). The training container is another function: it loads the model with Unsloth for each job and is not kept warm.

What happens in the GPU container on a cold request, in order (`app.py`, `inference.py`, `model_load_infer.py`):

1. **Containers start.** The request reaches the web API and the CPU router `infer` (each of them can be cold too), which call the GPU function. Modal starts a container on the GPU of that size (A10G for 4b, A100 for 12b) from the inference image, mounts the models and Hugging Face cache volumes and imports the app. This is Modal's time: it is **not** in `load_seconds`.
2. **The models volume is reloaded**, so an adapter trained a moment ago is visible (a failure here is only logged).
3. **The cache is checked.** The container holds at most one model, identified by its adapter directory and the file time of `adapter_config.json`. It is empty (or holds another model), so the loaded one is dropped and its GPU memory freed. From here until the model is ready is what `load_seconds` measures.
4. **The adapter is looked up.** `resolve_model_dir` turns `trained/<job_id>` into a directory of the models volume; without an `adapter_config.json` the request fails with "No trained model at …" (there is no silent fallback to the un-tuned base model).
5. **The libraries are imported** (`torch`, `peft`, `transformers`), once per container.
6. **The base model is loaded 4-bit.** `from_pretrained` reads the base model (`google/gemma-4-E4B-it` or `google/gemma-4-12B-it`) from the HF cache volume and quantizes it to nf4 (bfloat16 compute, double quantization) while placing it on GPU 0, the quantization the adapter was trained with. If the cache volume does not have the model yet (the first use ever, or a size that was never trained or served), it is **downloaded from Hugging Face first** (needs `HF_TOKEN`, see `ML_SIDECAR_HF_SECRET`), which makes that one request much slower; the volume keeps it for every later container. This step is most of `load_seconds`.
7. **The adapter is attached** with `peft` (not trainable, `eval()` mode) and the tokenizer is read from the adapter directory (the base model's if it is not there).
8. **The model is cached** in the container's memory, for the requests that follow.
9. **The prompt is generated:** tokenized, sampled up to `max_tokens`, decoded. When the answer is ready the container records itself in the `<app name>-inference` Dict, so the admin UI can list it (see above).

A warm request does steps 2 and 9 only, so its time is the generation. For a short reply that is seconds. A reply that does not end early (the model stops at its end-of-sequence token or at `max_tokens`) can take much longer, because 4-bit generation with `transformers` is not fast: read `output_tokens` next to the time.

Every request reports this, and the admin UI shows it:

- The answer of `POST /infer/query` has `cold_start` (`true` when the request loaded the model), `load_seconds` (steps 3 to 8; 0 when warm), `input_tokens` and `output_tokens`. `POST /v1/gb/training/models/{id}/infer` passes them on.
- The Go server stores `cold_start` and `load_seconds` with the other facts of the request (`gb_trained_model_inferences`; NULL for a failed request, a request made before gligabao 0.46.0, or an ML sidecar that does not report them).
- In the trained-model dialog of the admin UI, each request of the history has a line such as `cold start (model load 27.5s) · 3.2s without the model load · 12 tokens in / 34 out`, or `warm (model already loaded) · …`. The time *without the model load* is the total minus `load_seconds`, so it includes the container start of step 1 and the generation. The status line under the form shows the same after a request, and a dot above the prompt field shows whether a warm container holds this model.

How to read it: a long `load_seconds` is the model load (steps 3 to 8); a cold request with a short `load_seconds` but a long total spent its time starting containers; a slow warm request has a large `output_tokens` (a long reply that did not end) or waited behind another request of the same container.

Ways to get fewer cold requests (gligabao does none of them): a longer `ML_SIDECAR_INFER_IDLE_TIMEOUT_S` (up to 1200 seconds) at the price of GPU time while idle; a request every few minutes to keep a container up; or, in `app.py`, a minimum number of warm containers for `infer_4b`, which Modal bills all the time.

## Volumes

**Volumes** opens a table of the Modal volumes the app uses, read from the app's configuration (`config.py`, so the `ML_SIDECAR_VOL_*` settings are respected):

| Column | Meaning |
|---|---|
| Mounted at | Where the container sees it: `/data`, `/models`, `/eval`, `/root/.cache/huggingface`. |
| Volume | Its name in Modal (`gligabao-train-data`, ...). |
| In Modal | `found`, or `not created yet` (the first deploy creates it), or `error` with the reason as a tooltip. |
| Entries | How many files and directories its top level holds. |
| Created (UTC) | When Modal created it. |

The `/models` volume holds the trained models, one directory per completed job (`trained/<job_id>`, see [Trained models](ml_sidecar.md#where-the-weights-are-saved)); its **Entries** count is the number of top-level entries (`trained`, and `finetuned_model_<size>` if an older version saved one), not the number of models. `/root/.cache/huggingface` holds the downloaded base models.

It is read only: nothing is created, changed or deleted, and it works whether or not the app is deployed. It takes a few seconds because it runs `ml_sidecar/ai_train/providers/modal/list_volumes.py` in a subprocess (60 second limit). A volume that cannot be queried gets an `error` of its own and the others are still listed. Deleting or emptying volumes is not offered; use `modal volume` for that.

## The API

All routes are answered by the ML sidecar itself (they are not forwarded to the Modal app). `{name}` is the backend (`modal`), `{action}` is `deploy`, `stop` or `stop_inference`. The Go server passes them on under `/v1/gb/admin/api/ml_sidecar/backends/…` (the same paths after `/backends`), flushing the stream as it arrives; it adds nothing and keeps no state. They are for the admin UI, are only registered when the admin UI is on, and have no authentication of their own.

| ML sidecar route | Meaning |
|---|---|
| `GET /backends` | The backends with their live status: `name`, `configured`, `healthy`, and for `modal` also `deployed` (`true`, `false`, or `null` if unknown), `app`, `error` (when unhealthy) and `actions` (each action's state; `stop` is left out while the app is not deployed). The server's `GET /v1/gb/admin/api/ml_sidecar/backends` answers `{"backends": [...]}`, or an empty list with an `error` text when the ML sidecar is down. |
| `POST /backends/{name}/actions/{action}` | Start the action: `202` with its state; `409` if it (or the action it excludes) is running; `404` for an unknown backend or action. |
| `POST /backends/{name}/actions/{action}/cancel` | Cancel it: `202`, or `409` if it is not running. |
| `GET /backends/{name}/actions/{action}[?from=<i>]` | The state and the kept log lines with index `>= from`. |
| `GET /backends/{name}/actions/{action}/events[?from=<i>]` | The SSE stream above. |
| `GET /backends/{name}/apps` | `{"apps": [{"name", "app_id", "state", "n_running_tasks", "created_at", "inference", "active", "configured", "actions"}, ...]}`; `404` if the backend has none, `502` if they cannot be listed. |
| `…/backends/{name}/apps/{app}/actions/{action}[/events\|/cancel]` | The same routes as the actions above, for one app of this system (`404` for any other name). Without `apps/{app}` they act on the configured app. |
| `GET /backends/{name}/volumes` | `{"volumes": [{"mount_path", "name", "exists", "created_at"?, "entries"?, "error"?}, ...]}`; `404` if the backend has none, `502` if they cannot be listed. |

The state of an action:

```json
{
  "action": "deploy", "label": "Deploy the Modal app", "confirm": "", "danger": false,
  "run_id": 3, "state": "running",
  "stage": "building images", "stages": ["starting", "building images", "creating functions", "deployed"],
  "started_at": 1792188549.8, "finished_at": null, "exit_code": null, "error": "", "lines": 412
}
```

`state` is `idle` (never run since the ML sidecar started), `running`, `succeeded`, `failed` (`exit_code` and `error` say how) or `cancelled`. `run_id` grows with every run, which is how a client tells a new run from the old one. A run's log is kept until the next run of the same action starts. Only one run of an action goes at a time, and `deploy` and `stop` exclude each other.

```sh
curl -X POST   http://127.0.0.1:8787/backends/modal/actions/deploy           # start
curl -N        http://127.0.0.1:8787/backends/modal/actions/deploy/events    # follow it
curl           http://127.0.0.1:8787/backends/modal/actions/deploy           # state and log
curl -X POST   http://127.0.0.1:8787/backends/modal/actions/deploy/cancel    # cancel
curl           http://127.0.0.1:8787/backends/modal/volumes
```

## In the container

`Dockerfile.ml_sidecar` copies `ml_sidecar/ai_train/` to `/app/ai_train`, next to the ML sidecar, because that is the code a deploy uploads (and where the three scripts live); it is still not imported by the ML sidecar process. The scripts run as the image's non-root `ml_sidecar` user, which has no home directory and needs none. Only `modal` from `requirements_ml_sidecar.txt` is needed to deploy: the heavy ML packages are installed in Modal's images, not in the ML sidecar's.

## Troubleshooting

| What you see | Cause and fix |
|---|---|
| `app not deployed` | Nothing is deployed under `ML_SIDECAR_APP_NAME`: press **Deploy**. A training job started now fails with `502` saying the web URL of the app cannot be found. |
| `unhealthy`, tooltip `Token not found` / `Token ID is malformed` | The ML sidecar's `MODAL_TOKEN_ID` / `MODAL_TOKEN_SECRET` are wrong or revoked: create new ones with `modal token new` and restart the ML sidecar. |
| Deploy ends `failed` with `AuthError` in the log | The same: the tokens are not accepted. |
| Deploy ends `failed` about a missing secret | `ML_SIDECAR_HF_SECRET` or `ML_SIDECAR_AGENT_SECRET` names a Modal secret that does not exist: create it (`modal secret create`) or unset the variable. |
| Starting a research with the `modal` runner answers `503` "this Modal app has no research agent" | The app was deployed without the `gligabao_agent` binary: run `./run_build.sh` (or use the ML sidecar image), then **Redeploy**. |
| A research in Modal fails at once, naming `ANTHROPIC_API_KEY` | The agent's container has no keys: create the secret and set `ML_SIDECAR_AGENT_SECRET`, then redeploy. |
| A warm inference request is still slow | Read `output_tokens` in the request's history line (trained-model dialog): a reply that does not end until `max_tokens` takes long with 4-bit generation; otherwise it waited behind another request of the same container. A *cold* one is expected to be slow, see [Cold and warm starts](#cold-and-warm-starts). |
| Deploy stays at *starting* for long | Modal is slow to answer or the upload is large; read the log. If it is really stuck, **Cancel run** and try again. |
| **Stop** is missing | The app is not deployed (nothing to stop). |
| `409` on Deploy or Stop | A run of it, or of the action it excludes, is going: open **View log**, or cancel it. |
| **Volumes** shows `not created yet` | Normal before the first deploy. |
| The log is gone after a restart | By design: the log is in the ML sidecar's memory. The app itself is still in Modal. |

## Not included

- No authentication: the ML sidecar and the admin panel trust their network, so keep the ML sidecar on localhost or the private network and the admin UI off the public internet. Anyone who can reach these routes can deploy or stop the app.
- No scheduling or automatic redeploy: nothing deploys unless someone presses the button or calls the route.
- No volume management (create, empty, delete) and no log history across restarts.
- Only the `modal` backend has actions. Another backend gets them by overriding `Backend.actions()` (and `volumes()`); the admin UI draws whatever buttons the backend reports, using each action's `label`, `confirm` and `danger`. See [Python ml_sidecar](ml_sidecar.md#backends).
