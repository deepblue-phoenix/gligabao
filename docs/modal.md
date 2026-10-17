# Modal app lifecycle

`sidecar/backends/modal_backend.py`, `sidecar/backends/actions.py`, `sidecar/ai_train/deploy_modal.py`, `sidecar/ai_train/stop_modal.py`, `sidecar/ai_train/list_volumes.py`, `internal/api/admin/sidecar.go`, `admin_ui/src/actions.ts`

The training code runs on [Modal](https://modal.com) as an app (`gligabao-train` by default). The [sidecar](sidecar.md) is what talks to Modal, and since gligabao 0.34.0 it also manages the app's **lifecycle**: you can see whether the app is deployed, deploy and redeploy it, watch the build live, cancel it, stop the app, and list the volumes it uses, all from the **training** view of the admin UI or over HTTP. None of this needs the `modal` CLI, a login or a `~/.modal.toml`: the sidecar uses the Modal SDK with the tokens in its environment. The CLI commands in [Running on Modal](sidecar.md#remote-training-bootstrap) still work and do the same thing; the sidecar is the way to do it from the admin panel, or on a machine that has no CLI (the sidecar container).

The Go server stays out of it: it only passes the admin UI's requests on to the sidecar (see [the API](#the-api)).

## What you need

- The sidecar running with `MODAL_TOKEN_ID` and `MODAL_TOKEN_SECRET` (create them with `modal token new`; see the [sidecar's configuration](sidecar.md#configuration)). The same variables are what the deploy, stop and volume scripts use: the sidecar hands them to the subprocesses it starts.
- `SIDECAR_APP_NAME` set the same for the sidecar and the app (the default `gligabao-train` is fine). The sidecar passes its value to the scripts, so a deploy from the admin UI always deploys the app the sidecar looks up.
- The server running with the [admin UI](../README.md) and a reachable sidecar (`sidecar_url`).
- For fine-tuning, a Modal secret with `HF_TOKEN` named by `SIDECAR_HF_SECRET`, created in Modal beforehand (see [Running on Modal](sidecar.md#remote-training-bootstrap)). A deploy fails, and says so in its log, if the secret the app names does not exist.

## Where the state lives

- **In Modal:** the deployed app, its images, its volumes. This is the state that matters: it survives restarts of the sidecar, of its container, and of the machine, and `modal` on another computer sees the same app.
- **On the sidecar's disk:** nothing. With the tokens in the environment the sidecar never reads or writes `~/.modal.toml` (which the CLI creates with `modal token set`), so a container with no home directory or a read-only filesystem works.
- **In the sidecar's memory:** the state and log of the last run of each action (below). A sidecar restart forgets them and ends a deploy that is running; the next status check simply shows the app as deployed or not.

## What the admin UI shows

In the **training** view, the *Backends* list shows, for the `modal` backend:

| Badge | Meaning |
|---|---|
| `healthy` / `unhealthy` | Modal answers with the sidecar's tokens. `unhealthy` has the reason as a tooltip (for example `Token not found`, a network error, or a timeout after 10 seconds). |
| `app deployed` | The app's web function (`serve_api`) is found in Modal: training jobs can be started. |
| `app not deployed` | Modal answers but has no such app: deploy it. |
| `deployment unknown` | The lookup did not give a clear answer. |
| `deploy: <stage>…` | A deploy is running (the stage is its current step). |
| `last deploy failed` / `cancelled` | The last run of the action ended that way; **View log** shows why. |

Below it, the **apps list** (since 0.35.0): the active Modal apps of this system, one row each with its state (`deployed`, `ephemeral`, `initializing`, or `not deployed` for the configured app when it is not up), its running tasks, and the buttons **Redeploy** (**Deploy** when not deployed), **Last log** (the log of the last action run on that app; **View log** while it runs), **Stop** and **Volumes**. "Of this system" means the app named `SIDECAR_APP_NAME` and apps named `SIDECAR_APP_NAME-<suffix>` (for example a `-dev` copy); other apps in the Modal workspace are neither shown nor touchable. Deploy and Stop act on that row's app, and the log of one app is kept separately from the others'. The list comes from Modal (`list_apps.py`, the equivalent of `modal app list`) and takes a few seconds, so the previous list is shown meanwhile.

The buttons described below apply to each app row: **Deploy** (**Redeploy** once the app is deployed), **Stop** (red; only while the app is deployed), **Volumes**, and **View log** / **Last log** once an action has run. The list refreshes every 5 seconds while an action runs, and when you press *Refresh*.

## Deploy

**Deploy** asks for a confirmation, starts the run and opens the live log. What happens:

1. The sidecar starts `sidecar/ai_train/deploy_modal.py` in a subprocess, with `sidecar/ai_train/` as its working directory.
2. It imports `app.py` (the app definition) and calls the Modal SDK's `app.deploy()`, with Modal's build output switched on. This is what `modal deploy app.py` does.
3. Modal uploads the `ai_train/` directory (mounted into the containers at `/pkg`), builds the images that are not cached, creates the app's functions and the web endpoint, and creates any of the volumes that do not exist yet.
4. The script exits with `0` on success; the run is then `succeeded` and the badge turns to `app deployed`. Any error ends the script with a traceback and a final `Deploy failed: <error>` line in the log, and the run is `failed`.

The first deploy builds the CUDA and ML image (one Unsloth image for both model sizes), which takes several minutes; later deploys reuse Modal's image cache, and because the code is mounted last, a code change does not rebuild the dependency layers. Redeploying updates the app in place under the same name and web URL.

### Stages

The log dialog shows four stages as chips: *starting* (the process runs), *building images*, *creating functions*, *deployed*. The sidecar reads them from Modal's output (the lines about building images and creating objects) and they only move forward. They are a guide, not a protocol: if Modal changes its wording a stage may be skipped, so the log below is the source of truth.

## The live log

The dialog (`admin_ui/src/actions.ts`) has the state badge, the stage chips, the elapsed time, and the output of the command, one line each with its time since the start (`+12.3s`). Lines with `error`, `failed`, `traceback` or `exception` are red. **Follow** keeps the view at the end; scrolling up turns it off, ticking it scrolls down again. The dialog keeps the last 5000 lines.

Closing the dialog does not stop the run: it only stops watching. **View log** reopens it, also after a page reload, because the log lives in the sidecar. Behind it is a Server-Sent Events stream, so it is live and needs no polling:

- `log` events carry `lines`, a list of `{i, t, line}` (`i` the line's index, `t` its time in Unix seconds), with the SSE `id` of the last one.
- `state` events carry the state of the run (see [the API](#the-api)).
- The stream ends after the run is over. A client that loses the connection and reconnects gets the log again from the start (or `?from=<i>`) and skips the indices it already has.
- The output of the command is stdout and stderr merged; `\r` (progress bars) and `\n` both end a line, and colour codes are removed.

## Cancel

While a run is going the dialog has a **Cancel run** button. The sidecar sends the command `SIGINT` (so the SDK can clean up; the traceback of the interruption shows in the log) and kills it if it is still there 10 seconds later. The run ends as `cancelled` and can be started again. Cancelling before the deploy has finished normally leaves the previous deployment in place. What Modal had already built or cached on its side is not rolled back.

## Stop

**Stop** asks for a confirmation that names the consequences, then runs `sidecar/ai_train/stop_modal.py`, which does what `modal app stop` does: it looks the app up by name and tells Modal to stop it.

> **Warning:** Stopping the app terminates its running containers, **including training jobs and inference that are running**, and takes the web API offline: training and inference calls through the sidecar fail until you deploy again. The volumes (datasets, adapters, the Hugging Face cache) and the app's history are kept, and a stopped job's row stays in the database, where it is marked `failed` after the usual timeout (see [Not tracked yet](sidecar.md#remote-training-bootstrap)).

The Stop button is not offered while the app is not deployed, and it never runs at the same time as a deploy (`409`; cancel the deploy first). The SDK has no public `App.stop` yet, so the script makes the same two internal calls as the CLI (look up the app id by name, `AppStop`); a future `modal` 1.x release could change them, which would show as a failed `stop` with the error in the log. Starting the app again is a deploy.

## Warm inference containers

An inference request (`POST /infer/query`) runs in a GPU container of the `generate_4b` / `generate_12b` function. Until 0.39.0 every request loaded the base model and the adapter again (about the whole latency of a request); since 0.39.0 the container keeps the loaded model in memory and **stays up for `SIDECAR_INFER_IDLE_TIMEOUT_S` (default 300 s = 5 minutes) after its last request**, so the requests that follow skip the load. When no request arrives within that time Modal shuts it down by itself; the next request starts a container and loads the model again. The answer of `/infer/query` says which it was: `cold_start` and `load_seconds`.

- One model per container: a request for another trained model (or size) replaces the loaded one. A model retrained in place (the legacy `finetuned_model_<size>`) is noticed by its file time and loaded again.
- Several concurrent requests are handled one at a time per container, as before; Modal starts more containers when requests queue up (each loads the model, and each is billed while it is up).
- The timeout is part of the function's definition: **Redeploy** the app after changing `SIDECAR_INFER_IDLE_TIMEOUT_S` (or after upgrading to this version).
- The admin UI's app row shows a badge `N inference container(s) warm` (hover: function, model, requests, last use) and a **Stop inference** button that ends them at once (a request being answered fails; the app, the web API and training jobs are not touched). It runs `sidecar/ai_train/inference_containers.py`; the action is `stop_inference`, next to `deploy` and `stop`, and the `apps` listing has an `inference` list for every app.
- How it knows them: Modal does not say which function a container runs, so each inference container records itself (task id, model, requests, last use) in a Modal Dict named `<app name>-inference`, and the listing only trusts a record while Modal still lists that task. A container that is gone leaves a record that is removed at the next listing.

## Volumes

**Volumes** opens a table of the Modal volumes the app uses, read from the app's configuration (`config.py`, so the `SIDECAR_VOL_*` settings are respected):

| Column | Meaning |
|---|---|
| Mounted at | Where the container sees it: `/data`, `/models`, `/progress`, `/eval`, `/root/.cache/huggingface`. |
| Volume | Its name in Modal (`gligabao-train-data`, ...). |
| In Modal | `found`, or `not created yet` (the first deploy creates it), or `error` with the reason as a tooltip. |
| Entries | How many files and directories its top level holds. |
| Created (UTC) | When Modal created it. |

The `/models` volume holds the trained models, one directory per completed job (`trained/<job_id>`, see [Trained models](sidecar.md#where-the-weights-are-saved)); its **Entries** count is the number of top-level entries (`trained`, and `finetuned_model_<size>` if an older version saved one), not the number of models. `/root/.cache/huggingface` holds the downloaded base models.

It is read only: nothing is created, changed or deleted, and it works whether or not the app is deployed. It takes a few seconds because it runs `sidecar/ai_train/list_volumes.py` in a subprocess (60 second limit). A volume that cannot be queried gets an `error` of its own and the others are still listed. Deleting or emptying volumes is not offered; use `modal volume` for that.

## The API

All routes are answered by the sidecar itself (they are not forwarded to the Modal app). `{name}` is the backend (`modal`), `{action}` is `deploy`, `stop` or `stop_inference`. The Go server passes them on under `/v1/gb/admin/api/sidecar/backends/…` (the same paths after `/backends`), flushing the stream as it arrives; it adds nothing and keeps no state. They are for the admin UI, are only registered when the admin UI is on, and have no authentication of their own.

| Sidecar route | Meaning |
|---|---|
| `GET /backends` | The backends with their live status: `name`, `configured`, `healthy`, and for `modal` also `deployed` (`true`, `false`, or `null` if unknown), `app`, `error` (when unhealthy) and `actions` (each action's state; `stop` is left out while the app is not deployed). The server's `GET /v1/gb/admin/api/sidecar/backends` answers `{"backends": [...]}`, or an empty list with an `error` text when the sidecar is down. |
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

`state` is `idle` (never run since the sidecar started), `running`, `succeeded`, `failed` (`exit_code` and `error` say how) or `cancelled`. `run_id` grows with every run, which is how a client tells a new run from the old one. A run's log is kept until the next run of the same action starts. Only one run of an action goes at a time, and `deploy` and `stop` exclude each other.

```sh
curl -X POST   http://127.0.0.1:8787/backends/modal/actions/deploy           # start
curl -N        http://127.0.0.1:8787/backends/modal/actions/deploy/events    # follow it
curl           http://127.0.0.1:8787/backends/modal/actions/deploy           # state and log
curl -X POST   http://127.0.0.1:8787/backends/modal/actions/deploy/cancel    # cancel
curl           http://127.0.0.1:8787/backends/modal/volumes
```

## In the container

`Dockerfile.sidecar` copies `sidecar/ai_train/` to `/app/ai_train`, next to the sidecar, because that is the code a deploy uploads (and where the three scripts live); it is still not imported by the sidecar process. The scripts run as the image's non-root `sidecar` user, which has no home directory and needs none. Only `modal` from `requirements.txt` is needed to deploy: the heavy ML packages are installed in Modal's images, not in the sidecar's.

## Troubleshooting

| What you see | Cause and fix |
|---|---|
| `app not deployed` | Nothing is deployed under `SIDECAR_APP_NAME`: press **Deploy**. A training job started now fails with `502` saying the web URL of the app cannot be found. |
| `unhealthy`, tooltip `Token not found` / `Token ID is malformed` | The sidecar's `MODAL_TOKEN_ID` / `MODAL_TOKEN_SECRET` are wrong or revoked: create new ones with `modal token new` and restart the sidecar. |
| Deploy ends `failed` with `AuthError` in the log | The same: the tokens are not accepted. |
| Deploy ends `failed` about a missing secret | `SIDECAR_HF_SECRET` names a Modal secret that does not exist: create it (`modal secret create`) or unset the variable. |
| Deploy stays at *starting* for long | Modal is slow to answer or the upload is large; read the log. If it is really stuck, **Cancel run** and try again. |
| **Stop** is missing | The app is not deployed (nothing to stop). |
| `409` on Deploy or Stop | A run of it, or of the action it excludes, is going: open **View log**, or cancel it. |
| **Volumes** shows `not created yet` | Normal before the first deploy. |
| The log is gone after a restart | By design: the log is in the sidecar's memory. The app itself is still in Modal. |

## Not included

- No authentication: the sidecar and the admin panel trust their network, so keep the sidecar on localhost or the private network and the admin UI off the public internet. Anyone who can reach these routes can deploy or stop the app.
- No scheduling or automatic redeploy: nothing deploys unless someone presses the button or calls the route.
- No volume management (create, empty, delete) and no log history across restarts.
- Only the `modal` backend has actions. Another backend gets them by overriding `Backend.actions()` (and `volumes()`); the admin UI draws whatever buttons the backend reports, using each action's `label`, `confirm` and `danger`. See [Python sidecar](sidecar.md#backends).
