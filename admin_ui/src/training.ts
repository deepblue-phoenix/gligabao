// The "training" view: start a fine-tuning job of Gemma 4 on the sidecar (picking its dataset), the list of the training
// jobs in the database, and for the selected job a feed of its events and a graph of its loss. The events come from the
// server as SSE (../training/events), which forwards what the sidecar reports; d3 is loaded as a global by index.html.

import { initActionModal, showActionLog, startAction, type ActionState } from "./actions.js";

declare const d3: typeof import("d3");

interface Job {
  id: number; job_id: string; provider_job_id?: string; status: string; model_size: string; dataset: string;
  num_train_steps: number; last_step: number; last_loss: number | null; final_loss: number | null; error?: string;
  started_at: string; finished_at: string | null;
}
interface DatasetRecord {
  id: number; filename: string; size_bytes: number; num_records: number; sha256: string; status: "stored" | "synced";
  updated_at: string; synced_at: string | null;
}
interface FeedEvent {
  run_id: number; seq: number; type: "status" | "progress"; status?: string; max_steps?: number; final_loss?: number; error?: string;
  step?: number; loss?: number; learning_rate?: number; epoch?: number; elapsed_seconds?: number; timestamp?: number;
}
interface Point { step: number; loss: number }

const MAX_FEED_LINES = 500;
const PREVIEW_LINES = 100;
const FINISHED = ["completed", "failed"];

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
const fmtLoss = (v: number | null | undefined): string => (v == null ? "–" : v.toFixed(4));
const fmtBytes = (n?: number): string => (n == null ? "" : n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(1)} MB`);

function statusBadge(status: string): HTMLElement {
  const cls = status === "completed" ? "badge-ok" : status === "failed" ? "badge-error" : "badge-active";
  return node("span", `badge ${cls}`, status);
}

async function failure(res: Response): Promise<Error> {
  let msg = (await res.text()).trim();
  try { msg = (JSON.parse(msg) as { error?: string }).error ?? msg; } catch { /* not JSON */ }
  return new Error(`${res.status}: ${msg}`);
}

let jobs: Job[] = [];
let selected: number | null = null;
let points: Point[] = [];
let lastSeq = -1;
let perJob: EventSource | undefined; // the selected job's events (history, then live)
let allJobs: EventSource | undefined; // live events of every job, to keep the list current
let drawQueued = false;

//---------------------------------------------------------------------------------------------
// Jobs list

function renderJobs(): void {
  const body = el<HTMLTableElement>("training-jobs").tBodies[0];
  body.replaceChildren();
  el("training-jobs").hidden = jobs.length === 0;
  for (const j of jobs) {
    const tr = body.insertRow();
    tr.classList.toggle("selected", j.id === selected);
    tr.title = j.error ? j.error : "Show this job's feed and loss";
    tr.insertCell().textContent = String(j.id);
    tr.insertCell().appendChild(statusBadge(j.status));
    tr.insertCell().textContent = j.model_size;
    tr.insertCell().textContent = j.dataset || "(default)";
    const steps = tr.insertCell();
    steps.className = "num";
    steps.textContent = `${j.last_step} / ${j.num_train_steps}`;
    const loss = tr.insertCell();
    loss.className = "num";
    loss.textContent = fmtLoss(j.final_loss ?? j.last_loss);
    loss.title = j.final_loss != null ? "final loss" : "latest loss";
    tr.insertCell().textContent = fmtTime(j.started_at);
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
    if (selected === null && jobs.length > 0) {
      const running = jobs.find((j) => !FINISHED.includes(j.status));
      select((running ?? jobs[0]).id);
    }
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
  if (ev.type === "status" && ev.status) j.status = ev.status;
  if (ev.type === "progress" && ev.step != null) {
    j.last_step = ev.step;
    if (ev.loss != null) j.last_loss = ev.loss;
  }
  if (ev.type === "status" && ev.status && FINISHED.includes(ev.status)) { void loadJobs(); return; } // final loss, finish time
  renderJobs();
  renderDetailHead();
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
    start.addEventListener("click", () => {
      if (!confirm(a.confirm || `${a.label}?\n\nIt changes the remote backend and can take several minutes. The live log opens next.`)) return;
      void startAction(backend, name, title, () => void loadBackends()).catch((err) => alert(`Could not start ${name}: ${errText(err)}`));
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
    b.addEventListener("click", () => {
      if (!confirm(st.confirm || `${st.label}: ${a.name}?\n\nIt changes the app in Modal and can take several minutes. The live log opens next.`)) return;
      void startAction(backend, name, `${backend}: ${st.label} (${a.name})`, refresh, a.name).catch((err) => alert(`Could not start ${name}: ${errText(err)}`));
    });
  };
  start("deploy", a.active ? "Redeploy" : "Deploy");
  if (latest) {
    const l = latest;
    const running = l.st.state === "running";
    button(running ? "View log" : "Last log", `The log of the last action (${l.name}) on ${a.name}`)
      .addEventListener("click", () => showActionLog(backend, l.name, `${backend}: ${l.st.label} (${a.name})`, refresh, a.name));
  }
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

// Fills the dataset picker with the datasets of the server (the only place datasets are managed; a job's dataset travels
// to the sidecar with the job). It needs no sidecar.
async function loadDatasets(): Promise<void> {
  await checkSidecar();
  void loadBackends();
  const sel = el<HTMLSelectElement>("training-dataset");
  const status = el("training-run-status");
  const keep = sel.value;
  const entries = new Map<string, string>(); // filename -> label
  try {
    const res = await fetch("../training/datasets");
    if (!res.ok) throw await failure(res);
    renderDatasetRecords(((await res.json()) as { datasets: DatasetRecord[] }).datasets ?? [], entries);
  } catch (err) {
    status.className = "error";
    status.textContent = `Could not list the datasets: ${errText(err)}`;
  }
  sel.replaceChildren(new Option("(sidecar default dataset)", ""));
  for (const [name, label] of [...entries].sort((a, b) => a[0].localeCompare(b[0]))) sel.add(new Option(label, name));
  sel.value = [...sel.options].some((o) => o.value === keep) ? keep : "";
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
    entries?.set(d.filename, `${d.filename} (${fmtBytes(d.size_bytes)}, ${d.num_records} records)`);
    const tr = body.insertRow();
    tr.title = `sha256 ${d.sha256}\nClick to inspect the first ${PREVIEW_LINES} lines`;
    tr.addEventListener("click", () => void showDataset(d.filename));
    tr.insertCell().textContent = d.filename;
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
async function showDataset(filename: string): Promise<void> {
  const modal = el<HTMLDialogElement>("dataset-modal");
  const status = el("dataset-status");
  const content = el("dataset-content");
  el("dataset-title").textContent = filename;
  status.className = "muted";
  status.textContent = "Loading…";
  content.replaceChildren();
  if (!modal.open) modal.showModal();
  try {
    const res = await fetch(`../training/datasets/preview?${new URLSearchParams({ filename, lines: String(PREVIEW_LINES) })}`);
    if (!res.ok) throw await failure(res);
    const data = (await res.json()) as { filename: string; lines: string[]; num_records: number; truncated: boolean };
    if (!modal.open || el("dataset-title").textContent !== filename) return; // closed (or another one opened) while loading
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
    status.textContent = `Saved ${ds.filename}: ${ds.num_records} records, ${fmtBytes(ds.size_bytes)}. It is sent to the sidecar when a job starts on it.`;
    input.value = "";
    await loadDatasets(); // the table and the picker
    el<HTMLSelectElement>("training-dataset").value = ds.filename;
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
  const body = { num_train_steps: steps, model_size: el<HTMLSelectElement>("training-size").value, dataset: el<HTMLSelectElement>("training-dataset").value };
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
  if (!j) return;
  head.append(node("strong", "", `Job #${j.id}`), " ", statusBadge(j.status), " ",
    node("span", "muted", `${j.model_size} · ${j.dataset || "default dataset"} · ${j.job_id}`));
  if (j.error) head.append(node("div", "error", j.error));
}

function select(id: number): void {
  selected = id;
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
  for (const type of ["status", "progress"]) es.addEventListener(type, (e) => onJobEvent(JSON.parse((e as MessageEvent<string>).data) as FeedEvent));
}

function openAllJobs(): void {
  allJobs?.close();
  const es = new EventSource("../training/events");
  allJobs = es;
  es.onopen = () => void loadJobs();
  for (const type of ["status", "progress"]) es.addEventListener(type, (e) => applyToList(JSON.parse((e as MessageEvent<string>).data) as FeedEvent));
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
  el("training-upload").addEventListener("click", () => void uploadDataset());
  const datasetModal = el<HTMLDialogElement>("dataset-modal");
  el("dataset-close").addEventListener("click", () => datasetModal.close());
  datasetModal.addEventListener("click", (e) => { if (e.target === datasetModal) datasetModal.close(); });
  el("training-refresh").addEventListener("click", () => { void loadDatasets(); void loadBackends(); void loadJobs(); });
  // The SSE streams are open only while the view is shown (a browser allows few connections to a host).
  const sync = (): void => {
    if (view.hidden) { closeStreams(); return; }
    void loadDatasets();
    openAllJobs();
    openPerJob();
    scheduleDraw();
  };
  new MutationObserver(sync).observe(view, { attributes: true, attributeFilter: ["hidden"] });
  window.addEventListener("resize", scheduleDraw);
  void loadJobs();
  if (!view.hidden) sync();
}
