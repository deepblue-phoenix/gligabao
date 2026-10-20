// The "training" view: start a fine-tuning job of Gemma 4 on the sidecar (picking its dataset), the list of the training
// jobs in the database, and for the selected job a feed of its events and a graph of its loss. The events come from the
// server as SSE (../training/events), which forwards what the sidecar reports; d3 is loaded as a global by index.html.

import { alertDialog, confirmDialog } from "./dialogs.js";
import { auditCase, auditRun, initEvalLive, statusBadge as caseStatusBadge, verdictBadge, watchEval, type TrainedModelCase, type TrainedModelRun } from "./eval_live.js";
import { initActionModal, showActionLog, startAction, type ActionState } from "./actions.js";

declare const d3: typeof import("d3");

interface Job {
  id: number; job_id: string; provider_job_id?: string; status: string; model_size: string; dataset: string;
  num_train_steps: number; last_step: number; last_loss: number | null; final_loss: number | null; error?: string;
  started_at: string; finished_at: string | null;
}
interface TrainedModelFile { path: string; size_bytes: number; sha256?: string }
interface TrainedModel {
  id: number; name: string; run_id: number | null; job_id: string; base_model: string; model_size: string; dataset: string;
  dataset_sha256: string; num_examples: number; num_train_steps: number; final_loss: number | null; params: Record<string, unknown>;
  path: string; files: TrainedModelFile[]; size_bytes: number; created_at: string;
}
interface Inference {
  id: number; trained_model_id: number; prompt: string; response: string; status: "running" | "completed" | "failed"; error: string;
  max_tokens: number; temperature: number | null; input_tokens: number; output_tokens: number; cold_start: boolean | null;
  load_seconds: number; duration_ms: number; created_at: string; completed_at: string | null;
}
interface DatasetRecord {
  id: number; filename: string; size_bytes: number; num_records: number; sha256: string; status: "stored" | "synced";
  updated_at: string; synced_at: string | null;
  // from the dataset's manifest (<datasets_training dir>/<dir>/dataset.toml)
  name: string; description?: string; file: string; dir: string; version?: string;
}
interface DatasetProblem { dir: string; error: string }
interface FeedEvent {
  run_id: number; seq: number; type: "status" | "progress" | "log"; message?: string; status?: string; max_steps?: number; final_loss?: number; error?: string;
  step?: number; loss?: number; learning_rate?: number; epoch?: number; elapsed_seconds?: number; timestamp?: number;
}
interface Point { step: number; loss: number }

const MAX_SCORE = 5; // the judge's scale: 0 to 5
const EVAL_POLL_MS = 3000;
const MAX_FEED_LINES = 500;
const PREVIEW_LINES = 100;
const FINISHED = ["completed", "failed", "cancelled"];

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

function node<K extends keyof HTMLElementTagNameMap>(tag: K, cls = "", text = ""): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text) e.textContent = text; // textContent only: never inject server data as HTML
  return e;
}

const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));
const fmtTime = (iso: string): string => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toISOString().replace("T", " ").slice(0, 19);
};
// "5 minutes ago", "3 hours ago", "2 days ago": whichever unit fits best.
const fmtAgo = (iso: string): string => {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return iso;
  const mins = Math.max(0, Math.floor((Date.now() - t) / 60000));
  const [n, unit] = mins >= 1440 ? [Math.floor(mins / 1440), "day"] : mins >= 60 ? [Math.floor(mins / 60), "hour"] : [mins, "minute"];
  return `${n} ${unit}${n === 1 ? "" : "s"} ago`;
};
// "45s", "5m 12s", "3h 20m", "2d 4h": a length of time in seconds, with its two largest units.
const fmtSpan = (secs: number): string => {
  const s = Math.max(0, Math.floor(secs));
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  return d > 0 ? `${d}d ${h}h` : h > 0 ? `${h}h ${m}m` : m > 0 ? `${m}m ${s % 60}s` : `${s}s`;
};
// The duration of a job: start to finish, or start to now while it still runs.
const jobDuration = (j: Job): string => {
  const start = new Date(j.started_at).getTime();
  const end = j.finished_at ? new Date(j.finished_at).getTime() : Date.now();
  return Number.isNaN(start) || Number.isNaN(end) ? "" : fmtSpan((end - start) / 1000);
};
// A long name shortened to its first 10 and last 5 characters.
const shortName = (s: string): string => (s.length > 18 ? `${s.slice(0, 10)}...${s.slice(-5)}` : s);
const fmtLoss = (v: number | null | undefined): string => (v == null ? "–" : v.toFixed(4));
const fmtBytes = (n?: number): string => (n == null ? "" : n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(1)} MB`);

function statusBadge(status: string): HTMLElement {
  const cls = status === "completed" ? "badge-ok" : status === "failed" ? "badge-error" : status === "cancelled" ? "badge-unknown" : "badge-active";
  return node("span", `badge ${cls}`, status);
}

async function failure(res: Response): Promise<Error> {
  let msg = (await res.text()).trim();
  try { msg = (JSON.parse(msg) as { error?: string }).error ?? msg; } catch { /* not JSON */ }
  return new Error(`${res.status}: ${msg}`);
}

let jobs: Job[] = [];
let models: TrainedModel[] = [];
let selectedModel: number | null = null;
let inferring = false; // an inference request is in flight
let selected: number | null = null;
let points: Point[] = [];
let lastSeq = -1;
let perJob: EventSource | undefined; // the selected job's events (history, then live)
let allJobs: EventSource | undefined; // live events of every job, to keep the list current
let drawQueued = false;
const stopping = new Set<number>(); // runs a stop was requested of, until they end
let stopError = ""; // why the last stop request failed

//---------------------------------------------------------------------------------------------
// Jobs list

function renderJobs(): void {
  const body = el<HTMLTableElement>("training-jobs").tBodies[0];
  body.replaceChildren();
  el("training-jobs").hidden = jobs.length === 0;
  for (const j of jobs) {
    const tr = body.insertRow();
    tr.classList.toggle("selected", j.id === selected);
    tr.classList.toggle("job-completed", j.status === "completed");
    tr.title = j.error ? j.error : "Show this job's feed and loss";
    tr.insertCell().textContent = String(j.id);
    const statusCell = tr.insertCell();
    statusCell.appendChild(statusBadge(j.status));
    if (!FINISHED.includes(j.status)) {
      // First cube: the job is alive; it blinks during the setup (container, model load) and goes still once the steps start.
      // Second, brighter cube: the training steps are executing.
      const training = j.status === "training" || j.last_step > 0;
      const cube = node("span", training ? "running-cube idle" : "running-cube");
      cube.title = training ? "Initialization done" : "Setting up";
      statusCell.append(" ", cube);
      if (training) {
        const trainCube = node("span", "running-cube training");
        trainCube.title = "Training";
        statusCell.append(trainCube);
      }
    }
    tr.insertCell().textContent = j.model_size;
    tr.insertCell().textContent = j.dataset || "(default)";
    const steps = tr.insertCell();
    steps.className = "num";
    steps.textContent = `${j.last_step} / ${j.num_train_steps}`;
    const loss = tr.insertCell();
    loss.className = "num";
    loss.textContent = fmtLoss(j.final_loss ?? j.last_loss);
    loss.title = j.final_loss != null ? "final loss" : "latest loss";
    const started = tr.insertCell();
    started.textContent = fmtAgo(j.started_at);
    started.title = `${fmtTime(j.started_at)} UTC`;
    const dur = tr.insertCell();
    dur.className = "num";
    dur.textContent = jobDuration(j);
    if (!FINISHED.includes(j.status) && !j.finished_at) dur.dataset.jobRunning = String(j.id); // kept current by a timer
    tr.addEventListener("click", () => select(j.id));
  }
  el("training-jobs-status").textContent = jobs.length === 0 ? "No training jobs yet." : "";
}

async function loadJobs(): Promise<void> {
  try {
    const res = await fetch("../training/jobs");
    if (!res.ok) throw await failure(res);
    jobs = ((await res.json()) as { jobs: Job[] }).jobs ?? [];
    el("training-jobs-status").className = "muted";
    renderJobs();
    renderDetailHead();
  } catch (err) {
    el("training-jobs-status").className = "error";
    el("training-jobs-status").textContent = `Failed to load training jobs: ${errText(err)}`;
  }
}

// Applies a live event to the list without reloading it.
function applyToList(ev: FeedEvent): void {
  const j = jobs.find((x) => x.id === ev.run_id);
  if (!j) { void loadJobs(); return; } // a job started elsewhere
  if (ev.type === "log") return; // nothing in the list changes
  if (ev.type === "status" && ev.status) j.status = ev.status;
  if (ev.type === "progress" && ev.step != null) {
    j.last_step = ev.step;
    if (ev.loss != null) j.last_loss = ev.loss;
  }
  if (ev.type === "status" && ev.status && FINISHED.includes(ev.status)) { // final loss, finish time; a completed job leaves a trained model
    stopping.delete(ev.run_id);
    void loadJobs();
    if (ev.status === "completed") void loadModels();
    return;
  }
  renderJobs();
  renderDetailHead();
}

//---------------------------------------------------------------------------------------------
// Trained models

function renderModels(): void {
  const table = el<HTMLTableElement>("training-models");
  const body = table.tBodies[0];
  body.replaceChildren();
  table.hidden = models.length === 0;
  for (const m of models) {
    const tr = body.insertRow();
    tr.classList.toggle("selected", m.id === selectedModel);
    tr.title = "Open this model: its files, inference, evals and the past replies";
    tr.insertCell().textContent = String(m.id);
    const name = tr.insertCell();
    name.textContent = shortName(m.name);
    name.title = m.name; // the full name on hover
    name.className = "nowrap";
    tr.insertCell().textContent = m.model_size;
    const dataset = tr.insertCell();
    dataset.textContent = m.dataset || "(default)";
    dataset.title = dataset.textContent;
    dataset.className = "cell-clip";
    const steps = tr.insertCell();
    steps.className = "num";
    steps.textContent = String(m.num_train_steps);
    const loss = tr.insertCell();
    loss.className = "num";
    loss.textContent = fmtLoss(m.final_loss);
    const size = tr.insertCell();
    size.className = "num";
    size.textContent = `${fmtBytes(m.size_bytes)} · ${m.files.length} file${m.files.length === 1 ? "" : "s"}`;
    const trained = tr.insertCell();
    trained.textContent = fmtAgo(m.created_at);
    trained.title = `${fmtTime(m.created_at)} UTC`;
    trained.className = "nowrap";
    // not the row's click: it opens the dialog
    // Audit opens its own dialog for this model (the form and the past audits); Eval starts a run right away.
    const auditCell = tr.insertCell();
    const auditBtn = node("button", "small", AUDIT.button);
    auditBtn.type = "button";
    auditBtn.title = `Open the audit dialog of this model: run a training data audit and see the past ones. ${m.dataset ? AUDIT.tip(m.dataset) : "This model was trained on the sidecar's default dataset: there are no questions to ask"}`;
    auditBtn.addEventListener("click", (e) => { e.stopPropagation(); openAudit(m.id); });
    auditCell.append(auditBtn);
    const evalCell = tr.insertCell();
    const evalBtn = node("button", "small", EVAL.button);
    evalBtn.type = "button";
    evalBtn.title = m.dataset ? EVAL.tip(m.dataset) : "This model was trained on the sidecar's default dataset: there are no questions to ask";
    evalBtn.disabled = !m.dataset;
    evalBtn.addEventListener("click", (e) => { e.stopPropagation(); void startRun(EVAL, m, {}); });
    evalCell.append(evalBtn);
    tr.addEventListener("click", () => selectModel(m.id));
  }
  el("training-models-status").textContent = models.length === 0 ? "No trained models yet: one is saved when a job completes." : "";
}

async function loadModels(): Promise<void> {
  const status = el("training-models-status");
  try {
    const res = await fetch("../training/models");
    if (!res.ok) throw await failure(res);
    models = ((await res.json()) as { models: TrainedModel[] }).models ?? [];
    status.className = "muted";
    if (selectedModel !== null && !models.some((m) => m.id === selectedModel)) selectedModel = null;
    renderModels();
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load trained models: ${errText(err)}`;
  }
}

// Opens the model's dialog: how it was trained, its files, a form to run a prompt through it and the past requests.
function selectModel(id: number): void {
  selectedModel = id;
  renderModels();
  renderModelDetail();
  const modal = el<HTMLDialogElement>("model-modal");
  if (!modal.open) modal.showModal();
  void loadInferences(id);
  EVAL.model = id;
  void loadRuns(EVAL, id);
  warm = "unknown";
  drawWarm();
  void pollWarm(id);
}

function closeModelModal(): void {
  selectedModel = null;
  clearTimeout(warmTimer);
  clearTimeout(EVAL.timer); EVAL.runs = []; EVAL.model = null;
  renderModels();
}

// Opens the audit dialog of a model: the form to audit it and the list of its past audits.
function openAudit(id: number): void {
  const m = models.find((x) => x.id === id);
  if (!m) return;
  AUDIT.model = id;
  el("audit-title").textContent = `Audit: ${m.name} (#${m.id})`;
  renderRunForm(AUDIT, m);
  el("model-audits-status").textContent = "Loading the audits…";
  el("model-audits").replaceChildren();
  const modal = el<HTMLDialogElement>("audit-modal");
  if (!modal.open) modal.showModal();
  auditWarm = "unknown";
  drawAuditContainer();
  void pollAuditWarm(id);
  void loadRuns(AUDIT, id);
}

function closeAuditModal(): void {
  clearTimeout(AUDIT.timer); AUDIT.runs = []; AUDIT.model = null; AUDIT.status = undefined;
  clearTimeout(auditWarmTimer); auditWarm = "unknown"; el("audit-container").replaceChildren();
}

// The Modal inference container of the audited model, shown as a square in the dialog's head. It blinks while the container is
// starting: an audit is running but has no answer yet (the container boots and loads the model on its first question). Green: the
// model is loaded; amber: a container holds another model; grey: no container up. Polled while the dialog is open, faster while starting.
let auditWarm: Warm = "unknown";
let auditWarmTimer: number | undefined;
const AUDIT_STARTING_POLL_MS = 4000;

const auditStarting = (): boolean => AUDIT.runs.some((r) => runRunning(r) && r.num_answered === 0);

function drawAuditContainer(): void {
  const box = el("audit-container");
  if (AUDIT.model === null) return;
  const starting = auditStarting() && auditWarm !== "warm";
  const state = starting ? "starting" : auditWarm;
  const text = starting ? "Inference container starting… (loading the model)"
    : auditWarm === "warm" ? "Inference container ready"
    : auditWarm === "other" ? "Container up with another model"
    : auditWarm === "cold" ? "No inference container up"
    : "Container status unknown";
  box.className = `container-state cs-${state}`;
  box.title = starting ? "The audit is waiting for the Modal inference container to start and load the model; it can take minutes."
    : auditWarm === "warm" ? "Inference container up, model loaded: questions are answered right away."
    : auditWarm === "other" ? "An inference container is up but holds another model: this one is loaded first (shorter than a cold start)."
    : auditWarm === "cold" ? "No inference container is up: the first question of an audit starts one and loads the model (can take minutes)."
    : "Inference container status unknown.";
  const sq = node("span", "cs-sq");
  sq.setAttribute("aria-hidden", "true");
  box.replaceChildren(sq, node("span", "", text));
}

async function pollAuditWarm(id: number): Promise<void> {
  clearTimeout(auditWarmTimer);
  const m = models.find((x) => x.id === id);
  if (!m || AUDIT.model !== id) return;
  const r = await fetchWarm(m);
  if (AUDIT.model !== id) return;
  auditWarm = r.state;
  drawAuditContainer();
  auditWarmTimer = window.setTimeout(() => void pollAuditWarm(id), auditStarting() ? AUDIT_STARTING_POLL_MS : WARM_POLL_MS);
}

// The facts and files of the selected model, and the inference form, in the dialog. The history under it is drawn by renderInferences.
function renderModelDetail(): void {
  const m = models.find((x) => x.id === selectedModel);
  if (!m) return;
  el("model-title").textContent = `${m.name} (#${m.id})`;
  const facts: [string, string][] = [
    ["Base model", m.base_model || m.model_size],
    ["Dataset", `${m.dataset || "(default)"}${m.num_examples ? ` · ${m.num_examples} examples` : ""}`],
    ["Dataset sha256", m.dataset_sha256 || "–"],
    ["Training", `${m.num_train_steps} steps · final loss ${fmtLoss(m.final_loss)}`],
    ["Settings", Object.entries(m.params ?? {}).map(([k, v]) => `${k}=${String(v)}`).join(", ") || "–"],
    ["Job", m.run_id != null ? `#${m.run_id} (${m.job_id})` : m.job_id],
    ["Weights", `${m.path} on the sidecar's models volume`],
  ];
  const dl = node("dl");
  for (const [k, v] of facts) dl.append(node("dt", "", k), node("dd", "", v));

  const files = node("table");
  const head = files.createTHead().insertRow();
  for (const h of ["File", "Size", "sha256"]) head.appendChild(node("th", h === "Size" ? "num" : "", h));
  const fbody = files.createTBody();
  for (const f of m.files) {
    const tr = fbody.insertRow();
    tr.insertCell().textContent = f.path;
    const size = tr.insertCell();
    size.className = "num";
    size.textContent = fmtBytes(f.size_bytes);
    const sha = tr.insertCell();
    sha.textContent = f.sha256 ? f.sha256.slice(0, 12) : "";
    sha.title = f.sha256 ?? "";
  }
  const info = node("details", "model-info");
  info.append(node("summary", "", "Training details and files"), dl, files);

  const warmRow = node("div", "warm-row");
  warmDot = node("span", "warm-dot");
  warmRow.append(warmDot);
  drawWarm();

  const prompt = node("textarea");
  prompt.placeholder = "Prompt to run through this model, e.g. Check recent news about NVIDIA and buy 10 shares if it's positive";
  const tokens = node("input");
  tokens.type = "number"; tokens.value = "512"; tokens.min = "1"; tokens.max = "8192"; tokens.step = "1";
  const temp = node("input");
  temp.type = "number"; temp.value = "0.7"; temp.min = "0.05"; temp.max = "2"; temp.step = "0.05";
  const labelled = (text: string, input: HTMLElement): HTMLElement => { const l = node("label", "", text); l.append(input); return l; };
  const run = node("button", "", "Run inference");
  run.type = "button";
  run.disabled = inferring;
  const status = node("span", "muted");
  inferStatus = status;
  inferButton = run;
  run.addEventListener("click", () => {
    const p = prompt.value.trim();
    const n = Number.parseInt(tokens.value, 10), t = Number.parseFloat(temp.value);
    if (!p) { status.className = "error"; status.textContent = "Enter a prompt."; return; }
    if (!Number.isInteger(n) || n < 1 || !(t > 0)) { status.className = "error"; status.textContent = "Max tokens must be a positive integer and temperature above 0."; return; }
    void infer(m, p, n, t);
  });
  const form = node("div", "row training-form");
  form.append(labelled("Max tokens", tokens), labelled("Temperature", temp), run, status);

  el("model-detail").replaceChildren(info, warmRow, prompt, form);
  renderRunForm(EVAL, m);
  el("model-inferences").replaceChildren();
  el("model-inferences-status").textContent = "Loading the past requests…";
}

// Whether the Modal inference container of the open model is up ("warm": the model is loaded, so a prompt runs right away),
// polled from the sidecar's app list (the containers record themselves after each request and Modal shuts them down by itself
// a few minutes after the last one). "other": a container of the size is up but holds another model. "unknown": not known (yet).
type Warm = "unknown" | "cold" | "other" | "warm";
let warm: Warm = "unknown";
let warmInfo = "";
let warmDot: HTMLElement | undefined;
let warmTimer: number | undefined;
const WARM_POLL_MS = 15000;

function drawWarm(): void {
  const d = warmDot;
  if (!d) return;
  d.className = `warm-dot warm-${warm}`;
  d.title = warm === "warm" ? `Inference container up, model loaded: prompts run right away. ${warmInfo}`
    : warm === "other" ? "An inference container is up but holds another model: this one is loaded first (shorter than a cold start)."
    : warm === "cold" ? "No inference container is up: the first prompt starts one and loads the model (can take minutes)."
    : "Inference container status unknown.";
}

// The state of the Modal inference container of model m, from the sidecar's app list.
async function fetchWarm(m: TrainedModel): Promise<{ state: Warm; info: string }> {
  let state: Warm = "unknown", info = "";
  try {
    const res = await fetch("api/sidecar/backends/modal/apps");
    if (res.ok) {
      const apps = ((await res.json()) as { apps: AppInfo[] }).apps ?? [];
      const containers = apps.filter((a) => a.configured).flatMap((a) => a.inference ?? []);
      const mine = containers.find((c) => c.model_path === m.path || (c.adapter ?? "").endsWith(m.path));
      if (mine) {
        state = "warm";
        info = `${mine.requests ?? 0} request(s)` + (mine.last_used ? `, last used ${fmtTime(new Date(mine.last_used * 1000).toISOString())} UTC` : "") + ".";
      } else state = containers.some((c) => c.model_size === m.model_size) ? "other" : "cold";
    }
  } catch { /* unreachable: stays unknown */ }
  return { state, info };
}

async function checkWarm(m: TrainedModel): Promise<void> {
  const r = await fetchWarm(m);
  if (selectedModel !== m.id) return;
  warm = r.state; warmInfo = r.info;
  drawWarm();
}

// Checks now, then every WARM_POLL_MS while the model's dialog is open.
async function pollWarm(id: number): Promise<void> {
  clearTimeout(warmTimer);
  const m = models.find((x) => x.id === id);
  if (!m || selectedModel !== id) return;
  await checkWarm(m);
  if (selectedModel === id) warmTimer = window.setTimeout(() => void pollWarm(id), WARM_POLL_MS);
}

// Status and Run button of the open dialog's form (undefined when it was closed or redrawn): an inference outlives the dialog.
let inferStatus: HTMLElement | undefined;
let inferButton: HTMLButtonElement | undefined;

// The past inference requests of the model, newest first (the server stores every one: prompt, response, status, error).
async function loadInferences(modelID: number): Promise<void> {
  const status = el("model-inferences-status");
  try {
    const res = await fetch(`../training/models/${modelID}/inferences`);
    if (!res.ok) throw await failure(res);
    const list = ((await res.json()) as { inferences: Inference[] }).inferences ?? [];
    if (selectedModel !== modelID) return; // another model, or closed, while loading
    status.className = "muted";
    status.textContent = list.length === 0 ? "No inference requests to this model yet." : `${list.length} past request${list.length === 1 ? "" : "s"}, newest first:`;
    renderInferences(list);
  } catch (err) {
    if (selectedModel !== modelID) return;
    status.className = "error";
    status.textContent = `Failed to load the past requests: ${errText(err)}`;
  }
}

function inferenceBadge(status: string): HTMLElement {
  const cls = status === "completed" ? "badge-ok" : status === "failed" ? "badge-error" : "badge-active";
  return node("span", `badge ${cls}`, status);
}

// What a completed request cost in time: cold or warm, the model load, the generation, the token counts and the speed.
// "" while it runs or when it failed.
function inferenceStats(r: Inference): string {
  if (r.status !== "completed") return "";
  const parts: string[] = [];
  if (r.cold_start === true) parts.push(`cold start (model load ${r.load_seconds.toFixed(1)}s)`);
  else if (r.cold_start === false) parts.push("warm (model already loaded)");
  if (r.duration_ms) {
    const gen = r.duration_ms / 1000 - (r.cold_start ? r.load_seconds : 0);
    if (r.cold_start != null) parts.push(`${Math.max(gen, 0).toFixed(1)}s without the model load`);
  }
  if (r.input_tokens || r.output_tokens) parts.push(`${r.input_tokens} tokens in / ${r.output_tokens} out`);
  return parts.join(" · ");
}

function renderInferences(list: Inference[]): void {
  const box = el("model-inferences");
  box.replaceChildren();
  for (const r of list) {
    const item = node("article", "infer-item");
    const head = node("div", "infer-head");
    const when = node("span", "muted", `#${r.id} · ${fmtTime(r.created_at)} UTC`);
    const meta = [r.duration_ms ? `${(r.duration_ms / 1000).toFixed(1)}s` : "", r.max_tokens ? `max ${r.max_tokens} tokens` : "",
      r.temperature != null ? `temp ${r.temperature}` : ""].filter(Boolean).join(" · ");
    head.append(inferenceBadge(r.status), when, node("span", "muted", meta));
    const stats = inferenceStats(r);
    if (stats) item.append(node("div", "muted infer-stats", stats));
    item.append(head, node("div", "infer-label", "Prompt"), node("pre", "infer-prompt", r.prompt));
    if (r.status === "failed") {
      item.append(node("div", "infer-label", "Error"), node("pre", "infer-error", r.error || "(no message)"));
    } else if (r.status === "running") {
      item.append(node("div", "infer-label", "Response"), node("pre", "infer-response muted", "Waiting for the model… (or interrupted, if the server restarted)"));
    } else {
      const pre = node("pre", "infer-response collapsed", r.response === "" ? "(the model returned an empty response)" : r.response);
      pre.title = "Click to expand or collapse the whole response";
      pre.addEventListener("click", () => {
        if (window.getSelection()?.toString()) return; // selecting text to copy, not toggling
        pre.classList.toggle("collapsed");
      });
      item.append(node("div", "infer-label", "Response"), pre);
    }
    box.append(item);
  }
}

// Runs a prompt through a trained model on the sidecar. The first call after a while is slow: a GPU container starts and loads the model.
// The server stores the request, so the history is reloaded afterwards, showing the response (or the error) at its top.
async function infer(m: TrainedModel, prompt: string, maxTokens: number, temperature: number): Promise<void> {
  inferring = true;
  const status = inferStatus, run = inferButton;
  if (run) run.disabled = true;
  if (status) {
    status.className = "muted";
    status.textContent = warm === "warm" ? "Running…" : "Running… (the first request loads the model on a GPU: this can take minutes)";
  }
  const started = Date.now();
  let outcome: { text: string; cls: string };
  try {
    const res = await fetch(`../training/models/${m.id}/infer`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt, max_tokens: maxTokens, temperature }),
    });
    if (!res.ok) throw await failure(res);
    const out = (await res.json()) as { cold_start?: boolean; load_seconds?: number; input_tokens?: number; output_tokens?: number };
    const load = out.cold_start ? ` (loaded the model: ${Math.round(out.load_seconds ?? 0)}s)` : out.cold_start === false ? " (model was already loaded)" : "";
    const tokens = out.input_tokens !== undefined ? `, ${out.input_tokens} tokens in / ${out.output_tokens ?? 0} out` : "";
    outcome = { text: `Done in ${Math.round((Date.now() - started) / 1000)}s${load}${tokens}.`, cls: "muted" };
  } catch (err) {
    outcome = { text: `Inference failed: ${errText(err)}`, cls: "error" };
  } finally {
    inferring = false;
    if (run) run.disabled = false;
    if (inferButton) inferButton.disabled = false; // the form of a dialog opened again meanwhile
  }
  if (status?.isConnected) { status.className = outcome.cls; status.textContent = outcome.text; }
  if (selectedModel === m.id) { void pollWarm(m.id); await loadInferences(m.id); }
}

//---------------------------------------------------------------------------------------------
// Training data audits and evals of a trained model. Both ask the model questions and have an LLM judge its answers, a batch of
// pairs per call, and the server runs them in the background and stores their progress, so the dialog just polls while one runs.
//   - an AUDIT asks the model the questions of its OWN training dataset (the cases are things it was trained on): it checks that the
//     training took and finds bad training data; it measures memorization and fit, not generalization.
//   - an EVAL asks the cases of an eval (a named set of questions with the answers expected, served by ../training/eval-definitions), which
//     the model never saw: it measures how well the model generalizes. The eval named like the model's dataset is the default.
// They share the code below; a RunKind holds what differs (URLs, words, elements) and the state of one list in the dialog.

interface RunKind {
  path: RunKindPath; // URL segment: ../training/<path>/...
  noun: string; // "audit" or "eval"
  button: string; // the label of its button
  formId: string; statusId: string; listId: string; // the elements of the dialog's section
  model: number | null; // the trained model whose dialog is open for this kind (null: closed)
  runs: TrainedModelRun[];
  timer: number | undefined;
  open: Set<number>; // runs whose answers are expanded
  cases: Map<number, TrainedModelCase[]>; // their answers, as last fetched
  status: HTMLElement | undefined; // the message line of the dialog's form
  listKey: "audits" | "evals"; // the key of the list in GET .../models/{id}/<path>
  // What the button asks, as a tooltip, and what the "dataset changed" badge means.
  tip: (dataset: string) => string;
}
type RunKindPath = "audits" | "evals";

const newKind = (k: Omit<RunKind, "model" | "runs" | "timer" | "open" | "cases" | "status">): RunKind =>
  ({ ...k, model: null, runs: [], timer: undefined, open: new Set(), cases: new Map(), status: undefined });
const AUDIT = newKind({
  path: "audits", noun: "audit", button: "Audit", formId: "model-audits-form", statusId: "model-audits-status", listId: "model-audits",
  listKey: "audits",
  tip: (dataset) => `Ask this model the questions of its own training dataset ${dataset} and have the LLM judge how closely its answers reproduce the dataset's`,
});
const EVAL = newKind({
  path: "evals", noun: "eval", button: "Eval", formId: "model-evals-form", statusId: "model-evals-status", listId: "model-evals",
  listKey: "evals",
  tip: (dataset) => `Ask this model the questions of the eval named ${dataset} (cases it never saw) and have the LLM judge its answers`,
});
const KINDS: RunKind[] = [AUDIT, EVAL];

// What a run asked: its eval (an eval run) or its training dataset (an audit run).
const runSource = (e: TrainedModelRun): string => e.eval ?? e.dataset ?? "";
const runRunning = (e: TrainedModelRun): boolean => e.status === "running";
const runMean = (e: TrainedModelRun): number | null => (e.num_judged > 0 ? e.score_sum / e.num_judged : null);

// Starts a run of kind k on model m (the options the server understands: limit, batch_size, ...) and opens its dialog to follow it.
async function startRun(k: RunKind, m: TrainedModel, opts: { limit?: number; batch_size?: number; eval?: string }): Promise<void> {
  const say = (text: string, cls: string): void => {
    for (const e of [el("training-models-status"), k.status]) if (e?.isConnected) { e.className = cls; e.textContent = text; }
  };
  say(`Starting an ${k.noun} of model #${m.id}…`, "muted");
  try {
    const res = await fetch(`../training/models/${m.id}/${k.path}`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(opts),
    });
    if (!res.ok) throw await failure(res);
    const started = readRun(k, await res.json());
    k.open.add(started.id);
    if (k.model !== m.id) { if (k === AUDIT) openAudit(m.id); else selectModel(m.id); }
    else await loadRuns(k, m.id);
    say(`${capital(k.noun)} #${started.id} started: ${started.counts.total} questions.`, "muted");
    openRunLive(k, started, m); // watch it run
  } catch (err) {
    say(`Could not start the ${k.noun} of model #${m.id}: ${errText(err)}`, "error");
  }
}

// The run (or case) of that kind as the server sent it, in the shape the dialog works with (see auditRun).
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const readRun = (k: RunKind, raw: any): TrainedModelRun => (k.path === "audits" ? auditRun(raw) : (raw as TrainedModelRun));
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const readCases = (k: RunKind, raw: any): TrainedModelCase[] =>
  k.path === "audits" ? ((raw.case_runs ?? []) as Parameters<typeof auditCase>[0][]).map(auditCase) : ((raw.cases ?? []) as TrainedModelCase[]);

const capital = (s: string): string => s.charAt(0).toUpperCase() + s.slice(1);

async function cancelRun(kind: RunKindPath, id: number): Promise<void> {
  const k = KINDS.find((x) => x.path === kind) ?? AUDIT;
  try {
    const res = await fetch(`../training/${k.path}/${id}/cancel`, { method: "POST" });
    if (!res.ok && res.status !== 409) throw await failure(res); // 409: it ended meanwhile
  } catch (err) {
    void alertDialog(`Could not cancel ${k.noun} #${id}: ${errText(err)}`);
    return;
  }
  if (k.model !== null) void loadRuns(k, k.model);
}

// Opens the live view of a run: its questions and the model's answers as they come, then their scores.
function openRunLive(k: RunKind, e: TrainedModelRun, m?: TrainedModel): void {
  const model = m ?? models.find((x) => x.id === e.trained_model_id);
  watchEval(k.path, e.id, `${capital(k.noun)} #${e.id} · ${model ? `${model.name} (#${model.id})` : `model #${e.trained_model_id}`} · ${runSource(e)}`,
    () => { if (k.model !== null) void loadRuns(k, k.model); });
}

// The evals the server can run (GET ../training/eval-definitions), fetched once.
interface EvalDefinition { name: string; cases: { name: string }[] }
let evalDefinitions: Promise<EvalDefinition[]> | undefined;
function loadEvalDefinitions(): Promise<EvalDefinition[]> {
  evalDefinitions ??= fetch("../training/eval-definitions")
    .then(async (res) => { if (!res.ok) throw await failure(res); return ((await res.json()) as { evals: EvalDefinition[] }).evals ?? []; })
    .catch((err) => { evalDefinitions = undefined; throw err; });
  return evalDefinitions;
}

// The form above the list: how many questions (blank = all of the eval's or the dataset's), the judge's batch size, and the run
// button; an eval's also has the choice of the eval, the one named like the model's dataset by default.
function renderRunForm(k: RunKind, m: TrainedModel): void {
  const isEval = k.path === "evals";
  const limit = node("input");
  limit.type = "number"; limit.min = "1"; limit.step = "1"; limit.placeholder = !isEval && m.num_examples ? `all (${m.num_examples})` : "all";
  const batch = node("input");
  batch.type = "number"; batch.value = "10"; batch.min = "1"; batch.max = "25"; batch.step = "1";
  batch.title = "Question/answer pairs sent to the judge LLM in one call";
  const run = node("button", "", k.button);
  run.type = "button";
  const status = node("span", "muted");
  k.status = status;
  const evalSelect = node("select");
  const labelled = (text: string, input: HTMLElement): HTMLElement => { const l = node("label", "", text); l.append(input); return l; };
  const fields = [labelled("Questions", limit), labelled("Judge batch size", batch), run, status];
  if (isEval) {
    fields.unshift(labelled("Eval", evalSelect));
    run.disabled = true;
    status.textContent = "Loading the evals…";
    void loadEvalDefinitions().then((defs) => {
      if (!evalSelect.isConnected) return;
      const own = m.dataset.replace(/\.jsonl$/, "");
      if (!defs.some((d) => d.name === own)) evalSelect.append(new Option("Choose an eval…", ""));
      for (const d of defs) evalSelect.append(new Option(`${d.name} (${d.cases.length} cases)`, d.name, false, d.name === own));
      const sync = (): void => {
        const d = defs.find((x) => x.name === evalSelect.value);
        limit.placeholder = d ? `all (${d.cases.length})` : "all";
        run.disabled = !d;
      };
      evalSelect.addEventListener("change", sync);
      sync();
      status.textContent = defs.length === 0 ? "The server has no evals." : "";
    }).catch((err) => { status.className = "error"; status.textContent = `Could not load the evals: ${errText(err)}`; });
  } else {
    run.disabled = !m.dataset;
    if (!m.dataset) status.textContent = "This model was trained on the sidecar's default dataset: there are no questions to ask.";
  }
  run.addEventListener("click", () => {
    const opts: { limit?: number; batch_size?: number; eval?: string } = {};
    if (isEval) opts.eval = evalSelect.value;
    if (limit.value.trim() !== "") {
      const n = Number.parseInt(limit.value, 10);
      if (!Number.isInteger(n) || n < 1) { status.className = "error"; status.textContent = "Questions must be a positive integer (or blank for all)."; return; }
      opts.limit = n;
    }
    const b = Number.parseInt(batch.value, 10);
    if (!Number.isInteger(b) || b < 1 || b > 25) { status.className = "error"; status.textContent = "Batch size must be between 1 and 25."; return; }
    opts.batch_size = b;
    void startRun(k, m, opts);
  });
  el(k.formId).replaceChildren(...fields);
}

async function loadRuns(k: RunKind, modelID: number): Promise<void> {
  clearTimeout(k.timer);
  const status = el(k.statusId);
  try {
    const res = await fetch(`../training/models/${modelID}/${k.path}`);
    if (!res.ok) throw await failure(res);
    const list = (((await res.json()) as Record<string, unknown[]>)[k.listKey] ?? []).map((r) => readRun(k, r));
    // The answers of the expanded runs: the ones of a running run grow, so they are fetched again each time.
    await Promise.all(list.filter((e) => k.open.has(e.id) && (runRunning(e) || !k.cases.has(e.id))).map(async (e) => {
      const r = await fetch(`../training/${k.path}/${e.id}`);
      if (r.ok) k.cases.set(e.id, readCases(k, await r.json()));
    }));
    if (k.model !== modelID) return; // another model, or closed, while loading
    const wasStarting = k === AUDIT && auditStarting();
    k.runs = list;
    status.className = "muted";
    status.textContent = list.length === 0 ? `No ${k.noun}s of this model yet.` : `${list.length} ${k.noun}${list.length === 1 ? "" : "s"}, newest first:`;
    renderRuns(k);
    if (k === AUDIT) {
      drawAuditContainer();
      // A run just started: look at the container now rather than at the next poll.
      if (!wasStarting && auditStarting() && auditWarm !== "warm") void pollAuditWarm(modelID);
    }
    if (list.some(runRunning)) k.timer = window.setTimeout(() => void loadRuns(k, modelID), EVAL_POLL_MS);
  } catch (err) {
    if (k.model !== modelID) return;
    status.className = "error";
    status.textContent = `Failed to load the ${k.noun}s: ${errText(err)}`;
  }
}

function scoreClass(score: number | null): string {
  return score === null ? "score-none" : score >= 4 ? "score-good" : score >= 2 ? "score-mid" : "score-bad";
}

function renderRuns(k: RunKind): void {
  const m = models.find((x) => x.id === k.model);
  const box = el(k.listId);
  box.replaceChildren();
  for (const e of k.runs) {
    const item = node("article", "infer-item eval-card");
    const head = node("div", "infer-head");
    head.append(caseStatusBadge(e.status), node("span", "muted", `#${e.id} · ${fmtTime(e.created_at)} UTC`),
      node("span", "muted", `${runSource(e)} · batches of ${e.params.batch_size ?? "?"}${e.params.limit ? ` · first ${e.params.limit} questions` : ""}`));
    if (m && m.dataset_sha256 && e.dataset_sha256 && m.dataset_sha256 !== e.dataset_sha256) {
      const warn = node("span", "badge badge-unknown", "dataset changed since training");
      warn.title = `The dataset file read by this ${k.noun} is not the one the model was trained on (its sha256 differs)`;
      head.append(warn);
    }
    if (runRunning(e)) {
      const stop = node("button", "small danger", "Stop");
      stop.type = "button";
      stop.addEventListener("click", () => void cancelRun(k.path, e.id));
      head.append(stop);
    }
    const watch = node("button", "small", runRunning(e) ? "Watch live" : "Open");
    watch.type = "button";
    watch.title = runRunning(e) ? `Follow the questions and answers as the ${k.noun} runs` : "Every question with the model's answer and its score";
    watch.addEventListener("click", () => openRunLive(k, e, m));
    head.append(watch);
    item.append(head);

    const c = e.counts;
    const done = c.completed + c.errored + c.cancelled;
    const bar = node("progress");
    bar.max = Math.max(c.total, 1);
    bar.value = done;
    const mean = runMean(e);
    const parts = [`${done} / ${c.total} questions`];
    for (const [n, word] of [[c.passed ?? 0, "pass"], [c.failed ?? 0, "fail"], [c.errored, "errored"], [c.cancelled, "cancelled"], [c.pending, "pending"]] as const) {
      if (n > 0) parts.push(`${n} ${word}`);
    }
    if (mean !== null) {
      parts.push(`mean score ${mean.toFixed(2)} / ${MAX_SCORE}`, `${Math.round((100 * e.num_correct) / e.num_judged)}% correct`,
        `${e.num_correct} correct · ${e.num_partial} partial · ${e.num_incorrect} incorrect`);
    }
    const summary = node("div", "eval-summary", parts.join(" · "));
    if (mean !== null) summary.classList.add(scoreClass(mean));
    item.append(bar, summary);
    const judge = [e.judge_model ? `judge ${e.judge_model}` : "", `${e.judge_calls} judge call${e.judge_calls === 1 ? "" : "s"}`,
      e.judge_calls ? `${e.judge_input_tokens} tokens in / ${e.judge_output_tokens} out` : "", e.judge_calls ? `$${e.judge_cost_usd.toFixed(4)}` : "",
      e.finished_at ? `took ${fmtSpan((new Date(e.finished_at).getTime() - new Date(e.created_at).getTime()) / 1000)}` : ""].filter(Boolean).join(" · ");
    item.append(node("div", "muted infer-stats", judge));
    if (e.error) item.append(node("div", "infer-label", "Error"), node("pre", "infer-error", e.error));

    const details = node("details", "eval-answers");
    details.open = k.open.has(e.id);
    details.append(node("summary", "", "Answers, lowest score first"));
    details.addEventListener("toggle", () => {
      if (details.open === k.open.has(e.id)) return;
      if (details.open) { k.open.add(e.id); if (k.model !== null) void loadRuns(k, k.model); } else k.open.delete(e.id);
    });
    if (details.open) details.append(runCasesTable(k, k.cases.get(e.id)));
    item.append(details);
    box.append(item);
  }
}

// The answers of a run, the worst first: the question, the expected answer, the model's, the score and the judge's reason.
function runCasesTable(k: RunKind, cases: TrainedModelCase[] | undefined): HTMLElement {
  if (!cases) return node("p", "muted", "Loading the answers…");
  if (cases.length === 0) return node("p", "muted", "No answers yet: they appear as each batch is judged.");
  const rank = (i: TrainedModelCase): number => (i.status === "errored" ? -1 : i.score ?? -1);
  const sorted = [...cases].sort((a, b) => rank(a) - rank(b) || a.idx - b.idx);
  const table = node("table", "eval-table");
  const head = table.createTHead().insertRow();
  for (const h of ["#", "Question", k.path === "evals" ? "Expected answer" : "Dataset answer", "Model answer", "Status", "Score", "Judge's reason"]) head.appendChild(node("th", h === "Score" ? "num" : "", h));
  const body = table.createTBody();
  const text = (tr: HTMLTableRowElement, s: string, cls = ""): void => { const c = tr.insertCell(); c.className = `eval-text ${cls}`; c.textContent = s; };
  for (const i of sorted) {
    const tr = body.insertRow();
    const num = tr.insertCell();
    num.textContent = String(i.idx + 1);
    if (i.name) num.title = i.name;
    text(tr, i.input ? `${i.instruction}\n\n${i.input}` : i.instruction);
    text(tr, i.reference);
    const st = (): void => {
      const c = tr.insertCell();
      c.append(caseStatusBadge(i.status));
      if (i.verdict) c.append(" ", verdictBadge(i.verdict));
    };
    if (i.status === "errored") {
      text(tr, i.response || "(no answer)", "muted");
      st();
      tr.insertCell().className = "num";
      text(tr, i.error, "error");
    } else {
      text(tr, i.response === "" ? "(empty)" : i.response);
      st();
      const c = tr.insertCell();
      c.className = `num score ${scoreClass(i.score)}`;
      c.textContent = i.score === null ? "–" : `${i.score} / ${MAX_SCORE}`;
      text(tr, i.reason, "muted");
    }
  }
  const wrap = node("div", "table-scroll");
  wrap.append(table);
  return wrap;
}

//---------------------------------------------------------------------------------------------
// Starting a job

// Shows a notice and disables Run if the server's sidecar is not active. Returns whether it is.
async function checkSidecar(): Promise<boolean> {
  let active = true;
  try {
    const res = await fetch("api/sidecar/status");
    if (res.ok) active = ((await res.json()) as { sidecar_active?: boolean }).sidecar_active !== false;
  } catch { /* server unreachable: the other requests report it */ }
  el("training-sidecar-warning").hidden = active;
  el<HTMLButtonElement>("training-run").disabled = !active;
  return active;
}

interface BackendStatus {
  name: string; configured?: boolean; healthy?: boolean; deployed?: boolean | null; app?: string; error?: string;
  actions?: Record<string, ActionState>;
}

let backendsTimer: number | undefined;

// The buttons of a backend's actions (deploy, ...): start it, or follow the running / last run in the live log dialog.
function actionButtons(backend: string, actions: Record<string, ActionState>, deployed: boolean | null | undefined): HTMLElement[] {
  const out: HTMLElement[] = [];
  for (const [name, a] of Object.entries(actions)) {
    const title = `${backend}: ${a.label}`;
    const running = a.state === "running";
    if (running) out.push(node("span", "badge badge-active", `${name}: ${a.stage ?? "running"}…`));
    else if (a.state === "failed") out.push(node("span", "badge badge-error", `last ${name} failed`));
    else if (a.state === "cancelled") out.push(node("span", "badge badge-unknown", `last ${name} cancelled`));
    const start = node("button", a.danger ? "small danger" : "small", name === "deploy" && deployed ? "Redeploy" : a.label.split(" ")[0]);
    start.type = "button";
    start.disabled = running;
    start.title = a.label + (a.state === "idle" ? "" : " (again)");
    start.addEventListener("click", async () => {
      if (!(await confirmDialog(a.confirm || `${a.label}?\n\nIt changes the remote backend and can take several minutes. The live log opens next.`, { title: a.label, okLabel: a.label.split(" ")[0], danger: a.danger }))) return;
      void startAction(backend, name, title, () => void loadBackends()).catch((err) => alertDialog(`Could not start ${name}: ${errText(err)}`));
    });
    out.push(start);
    if (a.state !== "idle") {
      const log = node("button", "small", running ? "View log" : "Last log");
      log.type = "button";
      log.addEventListener("click", () => showActionLog(backend, name, title, () => void loadBackends()));
      out.push(log);
    }
  }
  return out;
}

// Lists the training backends the sidecar reports (name, healthy or not). For Modal, also whether its app is deployed
// (found in Modal) or still has to be (`modal deploy app.py`).
async function loadBackends(): Promise<void> {
  const list = el("training-backends-list");
  const items: HTMLElement[] = [];
  let running = false; // an action is running: keep the list current (e.g. after a page reload)
  clearTimeout(backendsTimer);
  try {
    const res = await fetch("api/sidecar/backends");
    if (!res.ok) throw new Error(String(res.status));
    const body = (await res.json()) as { backends?: BackendStatus[]; error?: string };
    for (const b of (body.backends ?? []).filter((x) => x.configured !== false)) {
      const li = node("li", "");
      li.appendChild(node("strong", "", b.name));
      const health = node("span", `badge ${b.healthy ? "badge-ok" : "badge-error"}`, b.healthy ? "healthy" : "unhealthy");
      if (b.error) health.title = b.error;
      li.append(" ", health);
      if (b.name === "modal") {
        // The Modal apps of this system, each with its own actions and volumes (listed from Modal, a few seconds).
        if (b.healthy) {
          const box = node("ul", "backend-apps");
          li.appendChild(box);
          if (lastApps) renderApps(box, b.name, lastApps); else box.appendChild(node("li", "muted", "Loading the Modal apps…"));
          void loadApps(b.name, box);
        }
      } else if (b.actions) li.append(...actionButtons(b.name, b.actions, b.deployed));
      if (b.name !== "modal" && Object.values(b.actions ?? {}).some((a) => a.state === "running")) running = true;
      items.push(li);
    }
    if (items.length === 0) items.push(node("li", "muted", body.error ? `unavailable (${body.error})` : "none"));
  } catch {
    items.push(node("li", "muted", "unavailable"));
  }
  list.replaceChildren(...items);
  if (running && !el("view-training").hidden) backendsTimer = window.setTimeout(() => void loadBackends(), 5000);
}

interface AppInfo {
  name: string; app_id: string | null; state: string; n_running_tasks: number; created_at: string | null;
  active: boolean; configured: boolean; actions: Record<string, ActionState>;
  inference?: { task_id: string; function?: string; model_size?: string; model_path?: string; adapter?: string; loaded_at?: number; last_used?: number; requests?: number }[];
}

let lastApps: AppInfo[] | undefined; // the last list, shown while the next one loads

// Fetches the backend's apps and shows them in box. While one of their actions runs the list keeps refreshing.
async function loadApps(backend: string, box: HTMLElement): Promise<void> {
  try {
    const res = await fetch(`api/sidecar/backends/${encodeURIComponent(backend)}/apps`);
    if (!res.ok) throw await failure(res);
    const apps = ((await res.json()) as { apps: AppInfo[] }).apps;
    lastApps = apps;
    if (!box.isConnected) return; // the list was redrawn meanwhile
    renderApps(box, backend, apps);
    if (apps.some((a) => Object.values(a.actions).some((x) => x.state === "running")) && !el("view-training").hidden) {
      clearTimeout(backendsTimer);
      backendsTimer = window.setTimeout(() => void loadBackends(), 5000);
    }
  } catch (err) {
    if (box.isConnected) box.replaceChildren(node("li", "error", `Could not list the ${backend} apps: ${errText(err)}`));
  }
}

function renderApps(box: HTMLElement, backend: string, apps: AppInfo[]): void {
  box.replaceChildren(...(apps.length ? apps.map((a) => appRow(backend, a)) : [node("li", "muted", "no active apps")]));
}

// One app: its state, and the buttons Redeploy (Deploy if it is not deployed), Last log, Stop and Volumes.
function appRow(backend: string, a: AppInfo): HTMLElement {
  const li = node("li", "backend-app");
  li.appendChild(node("strong", "", a.name));
  const state = node("span", `badge ${a.active ? "badge-ok" : "badge-active"}`, a.state);
  state.title = a.active ? `${a.app_id ?? ""}${a.created_at ? ` · created ${fmtTime(a.created_at)}` : ""}` : "Not found in Modal: deploy it with the Deploy button";
  li.append(" ", state);
  if (a.active) li.append(" ", node("span", "muted", `${a.n_running_tasks} running task${a.n_running_tasks === 1 ? "" : "s"}`));
  const warm = a.inference ?? [];
  if (warm.length) {
    const badge = node("span", "badge badge-ok", `${warm.length} inference container${warm.length === 1 ? "" : "s"} warm`);
    badge.title = warm.map((c) => `${c.function ?? "?"}: ${c.adapter ?? c.model_size ?? "?"}, ${c.requests ?? 0} request(s)` +
      (c.last_used ? `, last used ${fmtTime(new Date(c.last_used * 1000).toISOString())}` : "")).join("\n") +
      "\nIt shuts down by itself a few minutes after its last request.";
    li.append(" ", badge);
  }
  const refresh = () => void loadBackends();
  let latest: { name: string; st: ActionState } | undefined; // the action that ran last, for "Last log"
  for (const [name, st] of Object.entries(a.actions)) {
    if (st.state === "running") li.append(" ", node("span", "badge badge-active", `${name}: ${st.stage ?? "running"}…`));
    else if (st.state === "failed") li.append(" ", node("span", "badge badge-error", `last ${name} failed`));
    else if (st.state === "cancelled") li.append(" ", node("span", "badge badge-unknown", `last ${name} cancelled`));
    if (st.state !== "idle" && (!latest || (st.started_at ?? 0) > (latest.st.started_at ?? 0))) latest = { name, st };
  }
  const button = (text: string, title: string, cls = "small"): HTMLButtonElement => {
    const b = node("button", cls, text);
    b.type = "button";
    b.title = title;
    li.append(" ", b);
    return b;
  };
  const start = (name: string, text: string): void => {
    const st = a.actions[name];
    if (!st) return;
    const b = button(text, `${st.label} (${a.name})`, st.danger ? "small danger" : "small");
    b.disabled = st.state === "running";
    b.addEventListener("click", async () => {
      if (!(await confirmDialog(st.confirm || `${st.label}: ${a.name}?\n\nIt changes the app in Modal and can take several minutes. The live log opens next.`, { title: `${st.label}: ${a.name}`, okLabel: st.label.split(" ")[0], danger: st.danger }))) return;
      void startAction(backend, name, `${backend}: ${st.label} (${a.name})`, refresh, a.name).catch((err) => alertDialog(`Could not start ${name}: ${errText(err)}`));
    });
  };
  start("deploy", a.active ? "Redeploy" : "Deploy");
  if (latest) {
    const l = latest;
    const running = l.st.state === "running";
    button(running ? "View log" : "Last log", `The log of the last action (${l.name}) on ${a.name}`)
      .addEventListener("click", () => showActionLog(backend, l.name, `${backend}: ${l.st.label} (${a.name})`, refresh, a.name));
  }
  start("stop_inference", "Stop inference");
  start("stop", "Stop");
  button("Volumes", "The Modal volumes the app uses").addEventListener("click", () => void toggleVolumes(backend, a.name));
  return li;
}

interface VolumeInfo { mount_path: string; name: string; exists: boolean | null; created_at?: string | null; entries?: number; error?: string }

let volumesOpen: string | undefined; // backend (and app) whose volumes the panel shows

// Shows (or hides, on a second click) the list of the volumes a backend's app uses: where each is mounted, its name, whether
// it exists in Modal (a missing one is created by the first deploy), when it was created and how many entries it has.
async function toggleVolumes(backend: string, app = ""): Promise<void> {
  const panel = el("training-volumes");
  const key = `${backend}/${app}`, what = app || backend;
  if (volumesOpen === key) { volumesOpen = undefined; panel.hidden = true; return; }
  volumesOpen = key;
  panel.hidden = false;
  panel.replaceChildren(node("p", "muted", `Loading the ${what} volumes…`));
  try {
    const res = await fetch(`api/sidecar/backends/${encodeURIComponent(backend)}/volumes`);
    if (!res.ok) throw await failure(res);
    const vols = ((await res.json()) as { volumes: VolumeInfo[] }).volumes;
    if (volumesOpen !== key) return; // closed (or another one opened) while loading
    const table = node("table");
    const head = table.createTHead().insertRow();
    for (const h of ["Mounted at", "Volume", "In Modal", "Entries", "Created (UTC)"]) head.appendChild(node("th", h === "Entries" ? "num" : "", h));
    const body = table.createTBody();
    for (const v of vols) {
      const tr = body.insertRow();
      tr.insertCell().textContent = v.mount_path;
      tr.insertCell().textContent = v.name;
      const st = tr.insertCell();
      st.appendChild(v.error ? node("span", "badge badge-error", "error") : node("span", `badge ${v.exists ? "badge-ok" : "badge-active"}`, v.exists ? "found" : "not created yet"));
      if (v.error) st.title = v.error;
      else if (!v.exists) st.title = "Modal creates it on the first deploy";
      const n = tr.insertCell();
      n.className = "num";
      n.textContent = v.entries == null ? "" : String(v.entries);
      tr.insertCell().textContent = v.created_at ? fmtTime(v.created_at) : "";
    }
    const errors = vols.filter((v) => v.error).map((v) => `${v.name}: ${v.error}`);
    panel.replaceChildren(node("h3", "", `${what} volumes`), table, ...(errors.length ? [node("p", "error", errors[0])] : []));
  } catch (err) {
    if (volumesOpen === key) panel.replaceChildren(node("p", "error", `Could not list the ${what} volumes: ${errText(err)}`));
  }
}

// Fills the dataset picker with the datasets of the server (the subdirectories of its training datasets directory that have a
// manifest; a job's dataset travels to the sidecar with the job). It needs no sidecar.
async function loadDatasets(): Promise<void> {
  await checkSidecar();
  void loadBackends();
  const sel = el<HTMLSelectElement>("training-dataset");
  const status = el("training-run-status");
  const keep = sel.value;
  const entries = new Map<string, string>(); // dataset name -> label
  try {
    const res = await fetch("../training/datasets");
    if (!res.ok) throw await failure(res);
    const data = (await res.json()) as { datasets: DatasetRecord[]; problems?: DatasetProblem[] };
    renderDatasetRecords(data.datasets ?? [], entries);
    renderDatasetProblems(data.problems ?? []);
  } catch (err) {
    status.className = "error";
    status.textContent = `Could not list the datasets: ${errText(err)}`;
  }
  sel.replaceChildren();
  for (const [name, label] of [...entries].sort((a, b) => a[0].localeCompare(b[0]))) sel.add(new Option(label, name));
  if (sel.options.length === 0) sel.add(new Option("(no datasets)", ""));
  sel.value = [...sel.options].some((o) => o.value === keep) ? keep : sel.options[0].value;
}

// Lists the dataset directories that have a manifest but cannot be used (a bad manifest, a missing data file).
function renderDatasetProblems(problems: DatasetProblem[]): void {
  const box = el("training-dataset-problems");
  box.hidden = problems.length === 0;
  box.replaceChildren(...problems.map((p) => node("li", "error", `${p.dir}: ${p.error}`)));
}

//---------------------------------------------------------------------------------------------
// Uploading datasets (the server validates and keeps them, and sends them to the sidecar)

// Shows the uploaded datasets in the table; adds them to the picker's entries if given.
function renderDatasetRecords(list: DatasetRecord[], entries?: Map<string, string>): void {
  const table = el<HTMLTableElement>("training-datasets");
  const body = table.tBodies[0];
  body.replaceChildren();
  table.hidden = list.length === 0;
  for (const d of list) {
    entries?.set(d.name, `${d.name} (${fmtBytes(d.size_bytes)}, ${d.num_records} records)`);
    const tr = body.insertRow();
    tr.title = `${d.description ? d.description + "\n" : ""}sha256 ${d.sha256}\nClick to inspect the first ${PREVIEW_LINES} lines`;
    tr.addEventListener("click", () => void showDataset(d.name));
    tr.insertCell().textContent = d.name;
    tr.insertCell().textContent = `${d.dir}/${d.file}`;
    const st = tr.insertCell();
    st.appendChild(node("span", `badge ${d.status === "synced" ? "badge-ok" : "badge-active"}`, d.status === "synced" ? "on sidecar" : "stored"));
    st.title = d.status === "synced" ? "The sidecar has this version" : "Saved on the server; sent to the sidecar when a job starts on it";
    const n = tr.insertCell();
    n.className = "num";
    n.textContent = String(d.num_records);
    const size = tr.insertCell();
    size.className = "num";
    size.textContent = fmtBytes(d.size_bytes);
    tr.insertCell().textContent = fmtTime(d.updated_at);
  }
}

// Appends a JSON line to parent with its tokens in spans (json-key, json-str, json-num, json-lit, json-punct). It is a
// plain tokenizer, not a parser, so a line that is not valid JSON is still shown whole. Text goes in as textContent only.
const JSON_TOKEN = /("(?:\\.|[^"\\])*")(\s*:)?|\b(?:true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|[{}[\],:]/g;
function appendJSON(parent: HTMLElement, line: string): void {
  let last = 0;
  for (const m of line.matchAll(JSON_TOKEN)) {
    if (m.index > last) parent.append(line.slice(last, m.index));
    if (m[1] !== undefined) {
      parent.append(node("span", m[2] !== undefined ? "json-key" : "json-str", m[1]));
      if (m[2] !== undefined) parent.append(node("span", "json-punct", m[2]));
    } else {
      const t = m[0];
      const cls = /^[{}[\],:]$/.test(t) ? "json-punct" : /^(true|false|null)$/.test(t) ? "json-lit" : "json-num";
      parent.append(node("span", cls, t));
    }
    last = m.index + m[0].length;
  }
  if (last < line.length) parent.append(line.slice(last));
}

// Opens the modal with the first lines of a dataset's file, to inspect it.
async function showDataset(name: string): Promise<void> {
  const modal = el<HTMLDialogElement>("dataset-modal");
  const status = el("dataset-status");
  const content = el("dataset-content");
  el("dataset-title").textContent = name;
  status.className = "muted";
  status.textContent = "Loading…";
  content.replaceChildren();
  if (!modal.open) modal.showModal();
  try {
    const res = await fetch(`../training/datasets/preview?${new URLSearchParams({ name, lines: String(PREVIEW_LINES) })}`);
    if (!res.ok) throw await failure(res);
    const data = (await res.json()) as { name: string; lines: string[]; num_records: number; truncated: boolean };
    if (!modal.open || el("dataset-title").textContent !== name) return; // closed (or another one opened) while loading
    data.lines.forEach((line, i) => {
      const row = node("span", "ln");
      row.append(node("span", "ln-no", String(i + 1)));
      appendJSON(row, line);
      content.append(row);
    });
    status.textContent = data.truncated
      ? `First ${data.lines.length} lines of ${data.num_records} records`
      : `All ${data.lines.length} lines`;
  } catch (err) {
    status.className = "error";
    status.textContent = `Could not read the dataset: ${errText(err)}`;
  }
}

async function uploadDataset(): Promise<void> {
  const btn = el<HTMLButtonElement>("training-upload");
  const input = el<HTMLInputElement>("training-upload-file");
  const status = el("training-upload-status");
  const file = input.files?.[0];
  if (!file) {
    status.className = "error";
    status.textContent = "Choose a .jsonl file first.";
    return;
  }
  const form = new FormData();
  form.append("file", file, file.name);
  btn.disabled = true;
  status.className = "muted";
  status.textContent = `Uploading ${file.name}…`;
  try {
    const res = await fetch("../training/datasets", { method: "POST", body: form });
    if (!res.ok) throw await failure(res);
    const ds = (await res.json()) as DatasetRecord;
    status.textContent = `Saved dataset ${ds.name}: ${ds.num_records} records, ${fmtBytes(ds.size_bytes)}. It is sent to the sidecar when a job starts on it.`;
    input.value = "";
    await loadDatasets(); // the table and the picker
    el<HTMLSelectElement>("training-dataset").value = ds.name;
  } catch (err) {
    status.className = "error";
    status.textContent = `Upload failed: ${errText(err)}`;
  } finally {
    btn.disabled = false;
  }
}

async function run(): Promise<void> {
  const btn = el<HTMLButtonElement>("training-run");
  const status = el("training-run-status");
  const steps = Number.parseInt(el<HTMLInputElement>("training-steps").value, 10);
  if (!Number.isInteger(steps) || steps < 1) {
    status.className = "error";
    status.textContent = "Steps must be a positive integer.";
    return;
  }
  const dataset = el<HTMLSelectElement>("training-dataset").value;
  if (!dataset) {
    status.className = "error";
    status.textContent = "Choose a dataset first (add one to the training datasets directory, or upload it).";
    return;
  }
  const body = { num_train_steps: steps, model_size: el<HTMLSelectElement>("training-size").value, dataset };
  btn.disabled = true;
  status.className = "muted";
  status.textContent = "Starting…";
  try {
    const res = await fetch("../training/jobs", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!res.ok) throw await failure(res);
    const job = (await res.json()) as Job;
    status.textContent = `Started job #${job.id}.`;
    await loadJobs();
    select(job.id);
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to start the job: ${errText(err)}`;
  } finally {
    btn.disabled = !el("training-sidecar-warning").hidden;
  }
}

//---------------------------------------------------------------------------------------------
// Selected job: feed and loss graph

function renderDetailHead(): void {
  const j = jobs.find((x) => x.id === selected);
  const head = el("training-detail-head");
  head.replaceChildren();
  el("training-selected").hidden = !j; // nothing to show until a job is picked from the list
  if (!j) return;
  head.append(node("strong", "", `Job #${j.id}`), " ", statusBadge(j.status), " ",
    node("span", "muted", `${j.model_size} · ${j.dataset || "(no dataset)"} · ${j.job_id}`));
  if (j.error) head.append(node("div", j.status === "cancelled" ? "muted" : "error", j.error));
  if (!FINISHED.includes(j.status)) {
    const asked = stopping.has(j.id);
    const stop = node("button", "small danger", asked ? "Stopping…" : "Stop job");
    stop.type = "button";
    stop.disabled = asked;
    stop.title = "Ask the job to stop: it ends at its next step (during the model load: when that is done)";
    stop.addEventListener("click", () => void stopJob(j.id));
    head.append(" ", stop);
    if (asked) head.append(" ", node("span", "muted", "stop requested; it takes effect at the next step"));
    if (stopError) head.append(node("div", "error", stopError));
  }
}

async function stopJob(id: number): Promise<void> {
  if (!(await confirmDialog(`Stop job #${id}?\n\nIt ends at its next step; the adapters of a stopped job are not saved.`, { title: "Stop job", okLabel: "Stop job", danger: true }))) return;
  stopError = "";
  stopping.add(id);
  renderDetailHead();
  try {
    const res = await fetch(`../training/jobs/${id}/stop`, { method: "POST" });
    if (!res.ok) throw await failure(res);
  } catch (err) {
    stopping.delete(id);
    stopError = `Failed to stop the job: ${errText(err)}`;
    renderDetailHead();
  }
}

function select(id: number): void {
  selected = id;
  stopError = "";
  points = [];
  lastSeq = -1;
  el("training-feed").replaceChildren();
  perJob?.close();
  renderJobs();
  renderDetailHead();
  scheduleDraw();
  if (el("view-training").hidden) return; // opened when the view is shown
  openPerJob();
}

function feedLine(ev: FeedEvent): string {
  if (ev.type === "log") return ev.message ?? "";
  if (ev.type === "progress") {
    const parts = [`step ${ev.step ?? "?"}`, `loss ${fmtLoss(ev.loss)}`];
    if (ev.learning_rate != null) parts.push(`lr ${ev.learning_rate.toExponential(2)}`);
    if (ev.epoch != null) parts.push(`epoch ${ev.epoch.toFixed(2)}`);
    if (ev.elapsed_seconds != null) parts.push(`${Math.round(ev.elapsed_seconds)}s`);
    return parts.join(" · ");
  }
  let s = `status: ${ev.status}`;
  if (ev.max_steps != null) s += ` (${ev.max_steps} steps)`;
  if (ev.final_loss != null) s += ` · final loss ${fmtLoss(ev.final_loss)}`;
  if (ev.error) s += ` · ${ev.error}`;
  return s;
}

function addFeed(ev: FeedEvent): void {
  const feed = el("training-feed");
  const atBottom = feed.scrollHeight - feed.scrollTop - feed.clientHeight < 24;
  const line = node("div", `tr-line tr-${ev.type}${ev.status === "failed" ? " error" : ""}`);
  line.append(node("span", "muted", ev.timestamp ? new Date(ev.timestamp * 1000).toISOString().slice(11, 19) : ""), " ", feedLine(ev));
  feed.append(line);
  while (feed.childElementCount > MAX_FEED_LINES) feed.firstElementChild?.remove();
  if (atBottom) feed.scrollTop = feed.scrollHeight;
}

function onJobEvent(ev: FeedEvent): void {
  if (ev.run_id !== selected || ev.seq <= lastSeq) return;
  lastSeq = ev.seq;
  addFeed(ev);
  if (ev.type === "progress" && ev.step != null && ev.loss != null) {
    points.push({ step: ev.step, loss: ev.loss });
    scheduleDraw();
  }
  if (ev.type === "status") {
    const j = jobs.find((x) => x.id === ev.run_id);
    if (j && ev.status) j.status = ev.status;
    renderDetailHead();
    if (ev.status && FINISHED.includes(ev.status)) {
      stopping.delete(ev.run_id);
      perJob?.close(); // the job is over; EventSource would reconnect otherwise
      el("training-stream").textContent = "Job finished.";
    }
  }
}

function openPerJob(): void {
  perJob?.close();
  if (selected === null) return;
  const stream = el("training-stream");
  stream.textContent = "Connecting…";
  const es = new EventSource(`../training/events?run_id=${selected}`);
  perJob = es;
  es.onopen = () => { stream.textContent = "Live."; };
  es.onerror = () => { stream.textContent = "Connection lost, reconnecting…"; }; // EventSource retries by itself (with Last-Event-ID)
  for (const type of ["status", "progress", "log"]) es.addEventListener(type, (e) => onJobEvent(JSON.parse((e as MessageEvent<string>).data) as FeedEvent));
}

function openAllJobs(): void {
  allJobs?.close();
  const es = new EventSource("../training/events");
  allJobs = es;
  es.onopen = () => void loadJobs();
  for (const type of ["status", "progress", "log"]) es.addEventListener(type, (e) => applyToList(JSON.parse((e as MessageEvent<string>).data) as FeedEvent));
}

function closeStreams(): void {
  perJob?.close();
  allJobs?.close();
  perJob = allJobs = undefined;
}

//---------------------------------------------------------------------------------------------
// Loss graph

function scheduleDraw(): void {
  if (drawQueued) return;
  drawQueued = true;
  requestAnimationFrame(() => { drawQueued = false; drawLoss(); });
}

function drawLoss(): void {
  const box = el("training-loss");
  box.replaceChildren();
  if (points.length === 0) {
    box.append(node("p", "muted", selected === null ? "Select a job." : "No loss reported yet."));
    return;
  }
  const W = box.clientWidth || 800, H = 280, m = { top: 12, right: 24, bottom: 36, left: 56 };
  const job = jobs.find((j) => j.id === selected);
  const maxStep = Math.max(job?.num_train_steps ?? 0, ...points.map((p) => p.step));
  const x = d3.scaleLinear().domain([0, maxStep]).range([m.left, W - m.right]);
  const [lo, hi] = d3.extent(points, (p) => p.loss) as [number, number];
  const pad = (hi - lo) * 0.1 || hi * 0.1 || 1;
  const y = d3.scaleLinear().domain([Math.max(0, lo - pad), hi + pad]).nice().range([H - m.bottom, m.top]);
  const svg = d3.create("svg").attr("viewBox", [0, 0, W, H]).attr("width", "100%").attr("height", H);
  svg.append("g").attr("class", "history-axis").attr("transform", `translate(0,${H - m.bottom})`).call(d3.axisBottom(x).ticks(Math.max(2, Math.floor(W / 100))));
  svg.append("g").attr("class", "history-axis").attr("transform", `translate(${m.left},0)`).call(d3.axisLeft(y).ticks(6));
  svg.append("text").attr("class", "history-label").attr("x", (m.left + W - m.right) / 2).attr("y", H - 4).attr("text-anchor", "middle").text("step");
  svg.append("text").attr("class", "history-label").attr("transform", `translate(12,${(m.top + H - m.bottom) / 2}) rotate(-90)`).attr("text-anchor", "middle").text("loss");
  const line = d3.line<Point>().x((p) => x(p.step)).y((p) => y(p.loss));
  svg.append("path").attr("class", "tr-loss-line").attr("d", line(points));
  const tip = svg.append("g").attr("class", "tr-tip").style("display", "none");
  const tipText = tip.append("text").attr("class", "history-label").attr("dy", "-0.6em").attr("text-anchor", "middle");
  svg.append("g").selectAll("circle").data(points).join("circle")
    .attr("class", "history-dot tr-dot").attr("cx", (p) => x(p.step)).attr("cy", (p) => y(p.loss)).attr("r", 3.5)
    .on("mouseenter", (_e, p) => { tip.style("display", null).attr("transform", `translate(${x(p.step)},${y(p.loss)})`); tipText.text(`step ${p.step}: ${p.loss.toFixed(4)}`); })
    .on("mouseleave", () => tip.style("display", "none"));
  box.append(svg.node() as SVGSVGElement);
}

//---------------------------------------------------------------------------------------------

export function initTraining(): void {
  const view = el("view-training");
  initActionModal();
  el("training-run").addEventListener("click", () => void run());
  for (const b of Array.from(document.querySelectorAll<HTMLButtonElement>("button.help-btn"))) {
    b.addEventListener("click", () => el<HTMLDialogElement>(b.dataset.help ?? "").showModal());
  }
  for (const d of Array.from(document.querySelectorAll<HTMLDialogElement>("dialog.help-modal"))) {
    d.querySelector("[data-close-help]")?.addEventListener("click", () => d.close());
    d.addEventListener("click", (e) => { if (e.target === d) d.close(); });
  }
  el("training-upload").addEventListener("click", () => void uploadDataset());
  const datasetModal = el<HTMLDialogElement>("dataset-modal");
  const modelModal = el<HTMLDialogElement>("model-modal");
  initEvalLive(cancelRun);
  el("model-close").addEventListener("click", () => modelModal.close());
  modelModal.addEventListener("close", closeModelModal);
  modelModal.addEventListener("click", (e) => { if (e.target === modelModal) modelModal.close(); });
  const auditModal = el<HTMLDialogElement>("audit-modal");
  el("audit-close").addEventListener("click", () => auditModal.close());
  auditModal.addEventListener("close", closeAuditModal);
  auditModal.addEventListener("click", (e) => { if (e.target === auditModal) auditModal.close(); });
  el("dataset-close").addEventListener("click", () => datasetModal.close());
  datasetModal.addEventListener("click", (e) => { if (e.target === datasetModal) datasetModal.close(); });
  el("training-refresh").addEventListener("click", () => { void loadDatasets(); void loadBackends(); void loadJobs(); void loadModels(); });
  // The SSE streams are open only while the view is shown (a browser allows few connections to a host).
  const sync = (): void => {
    if (view.hidden) { closeStreams(); return; }
    void loadDatasets();
    void loadModels();
    openAllJobs();
    openPerJob();
    scheduleDraw();
  };
  new MutationObserver(sync).observe(view, { attributes: true, attributeFilter: ["hidden"] });
  window.addEventListener("resize", scheduleDraw);
  // The "ago" and the duration of the running jobs advance without a reload of the list.
  setInterval(() => {
    if (view.hidden) return;
    const body = el<HTMLTableElement>("training-jobs").tBodies[0];
    body.querySelectorAll<HTMLElement>("td[data-job-running]").forEach((c) => {
      const j = jobs.find((x) => x.id === Number(c.dataset.jobRunning));
      if (j) c.textContent = jobDuration(j);
    });
  }, 1000);
  setInterval(() => { if (!view.hidden) renderJobs(); }, 30000); // refreshes the "ago" texts
  void loadJobs();
  void loadModels();
  if (!view.hidden) sync();
}
