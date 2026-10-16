// The "training" view: start a fine-tuning job of Gemma 4 on the sidecar (picking its dataset), the list of the training
// jobs in the database, and for the selected job a feed of its events and a graph of its loss. The events come from the
// server as SSE (api/training/events), which forwards what the sidecar reports; d3 is loaded as a global by index.html.

declare const d3: typeof import("d3");

interface Job {
  id: number; job_id: string; provider_job_id?: string; status: string; model_size: string; dataset: string;
  num_train_steps: number; last_step: number; last_loss: number | null; final_loss: number | null; error?: string;
  started_at: string; finished_at: string | null;
}
interface Dataset { filename: string; size_bytes?: number; modified_at?: string }
interface FeedEvent {
  run_id: number; seq: number; type: "status" | "progress"; status?: string; max_steps?: number; final_loss?: number; error?: string;
  step?: number; loss?: number; learning_rate?: number; epoch?: number; elapsed_seconds?: number; timestamp?: number;
}
interface Point { step: number; loss: number }

const MAX_FEED_LINES = 500;
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
    const res = await fetch("api/training/jobs");
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

async function loadDatasets(): Promise<void> {
  const sel = el<HTMLSelectElement>("training-dataset");
  const status = el("training-run-status");
  const keep = sel.value;
  try {
    const res = await fetch("api/training/datasets");
    if (!res.ok) throw await failure(res);
    const list = ((await res.json()) as { datasets: Dataset[] }).datasets ?? [];
    sel.replaceChildren(new Option("(sidecar default dataset)", ""));
    for (const d of list.sort((a, b) => a.filename.localeCompare(b.filename))) {
      const o = new Option(d.size_bytes != null ? `${d.filename} (${fmtBytes(d.size_bytes)})` : d.filename, d.filename);
      sel.add(o);
    }
    sel.value = [...sel.options].some((o) => o.value === keep) ? keep : "";
  } catch (err) {
    status.className = "error";
    status.textContent = `Could not list the sidecar's datasets: ${errText(err)}`;
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
    const res = await fetch("api/training/jobs", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!res.ok) throw await failure(res);
    const job = (await res.json()) as Job;
    status.textContent = `Started job #${job.id}.`;
    await loadJobs();
    select(job.id);
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to start the job: ${errText(err)}`;
  } finally {
    btn.disabled = false;
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
  const es = new EventSource(`api/training/events?run_id=${selected}`);
  perJob = es;
  es.onopen = () => { stream.textContent = "Live."; };
  es.onerror = () => { stream.textContent = "Connection lost, reconnecting…"; }; // EventSource retries by itself (with Last-Event-ID)
  for (const type of ["status", "progress"]) es.addEventListener(type, (e) => onJobEvent(JSON.parse((e as MessageEvent<string>).data) as FeedEvent));
}

function openAllJobs(): void {
  allJobs?.close();
  const es = new EventSource("api/training/events");
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
  el("training-run").addEventListener("click", () => void run());
  el("training-refresh").addEventListener("click", () => { void loadDatasets(); void loadJobs(); });
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
