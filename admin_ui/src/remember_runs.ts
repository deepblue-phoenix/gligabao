// The "remember runs" view (see docs/remember.md#run-log): per database, how many runs of the remember pipeline are in
// each status and the list of the runs, newest first. Clicking a status tile filters the list by it; clicking a run shows
// its parameters and how each stage went (status, error, time, inferences) and a button for each stage that ran, which loads (only then) and opens a modal with what the
// stage produced (its outputs, kept for debugging: see docs/remember.md#stage-outputs).

interface Stage {
  status: string; error: string | null; started_at: string | null; finished_at: string | null; inference_ids: number[];
}
interface Run {
  id: number; user_id: string; dataset: string; kind: string; params: Record<string, unknown>; artifact_id: number | null;
  status: string; error: string | null; started_at: string; finished_at: string | null; stages: Record<string, Stage>;
}
interface StageOutput { id: number; run_id: number; stage: string; name: string; label: string; output: unknown; created_at: string }
interface Counts { total: number; started: number; succeeded: number; failed: number }
interface Response { counts: Counts; stages: string[]; runs: Run[]; total: number; limit: number; offset: number }
interface Filter { status?: string; dataset?: string }

const PAGE = 100;

// What each stage of the pipeline does, for the "?" button of the stages table (see docs/remember.md#how-it-works).
const STAGE_HELP: Record<string, string> = {
  classify_complexity: "Asks Jev (a small fast model) whether the text is simple, medium or complex, i.e. how many entities it will probably yield. Advisory only: the result is logged, a failure never fails the run. Only runs when a Jev API key is configured.",
  chunk_text: "Splits a long text into chunks that the later stages process one after the other. A text that fits in one chunk is not split.",
  extract_graph: "The LLM reads the text (or a chunk) and returns the entities (name, type, description) and the edges, i.e. facts relating two entities, with the time each is valid. The raw answer is kept in the outputs, so a malformed one can be inspected.",
  summarize_chunk: "The LLM writes a short summary (2 to 4 sentences) of each chunk of a chunked text. The summary is stored and embedded so it can be searched. Not used for a text that is a single chunk.",
  load_candidates: "Looks up the facts already stored in the dataset that the new facts might repeat or contradict (stored live edges touching the same entities, found by name and by vector similarity). Embeds the endpoints of the new facts.",
  resolve_edges: "Compares the new edges with the candidate facts: exact repeats are detected without the LLM; the LLM flags the remaining duplicates (merged into the stored fact) and contradictions (the old fact is closed). Skipped when there is nothing to compare with.",
  store: "Writes everything in one transaction: the raw text as an artifact, the entities, the edges (merged or closed as decided), the session links, and the embeddings of entities, facts and chunks. If anything before the commit fails, nothing is stored.",
  store_series: "Stores the timeseries the caller supplied with the text and anchors them to the artifact and to the entities and edges their selectors match. Only runs when the request has a timeseries.",
  resolve_geo: "Finds which entities of the text are places (via the LLM) and asks the geo resolver (OpenStreetMap Nominatim by default) for their coordinates. Runs after the text is stored; a failure does not fail the run. Only runs when geo is turned on.",
  web_search: "Searches the web for the key entities of the text and remembers the results as web_search artifacts with their own entities and edges. Runs after the text is stored; a failure does not fail the run. Only runs when the request asks for web_search.",
  fetch_series: "The LLM decides which timeseries provider can supply a series for which entity of the text, the providers are called, and the series are stored and anchored to the entity. Runs after the text is stored; a failure does not fail the run. Only runs with timeseries.fetch.",
  observe_series: "Generates a short text from the statistics of each series stored in this run, extracts a graph from it and stores it as a series_observation artifact. Runs last; a failure does not fail the run. Only runs with timeseries.observations.",
};
const STATUSES = ["started", "succeeded", "failed"] as const;

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

const fmtInt = (n: number): string => n.toLocaleString("en-US");
const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));

function fmtTime(iso: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toISOString().replace("T", " ").slice(0, 19);
}

function fmtDuration(from: string | null, to: string | null): string {
  if (!from || !to) return "";
  const ms = new Date(to).getTime() - new Date(from).getTime();
  if (Number.isNaN(ms)) return "";
  return ms < 1000 ? `${Math.max(0, Math.round(ms))} ms` : `${(ms / 1000).toFixed(2)} s`;
}

function statusBadge(status: string): HTMLElement {
  const cls = status === "succeeded" ? "badge badge-ok" : status === "failed" ? "badge badge-error" : "badge badge-unknown";
  return node("span", cls, status.replace("_", " "));
}

function headRow(table: HTMLTableElement, cols: [string, boolean][]): void {
  const head = table.createTHead().insertRow();
  for (const [label, num] of cols) head.appendChild(node("th", num ? "num" : "", label));
}

interface Panel {
  filter: Filter;
  offset: number;
  status: HTMLElement;
  stats: HTMLElement;
  filterBar: HTMLElement;
  table: HTMLTableElement;
  body: HTMLTableSectionElement;
  pager: HTMLElement;
}

const panels = new Map<string, Panel>();

function makePanel(name: string): { box: HTMLElement; panel: Panel } {
  const box = node("div", "mr-db");
  box.appendChild(node("h3", "", `${name} database`));
  const stats = box.appendChild(node("dl", "stats rr-stats"));
  const status = box.appendChild(node("p", "muted"));
  const filterBar = box.appendChild(node("div", "row mr-filter"));
  const table = box.appendChild(node("table", "clickable"));
  table.hidden = true;
  headRow(table, [["ID", true], ["Started (UTC)", false], ["User", false], ["Dataset", false], ["Kind", false], ["Status", false],
    ["Duration", true], ["Artifact", true]]);
  const body = table.createTBody();
  const pager = box.appendChild(node("div", "row mr-pager"));
  return { box, panel: { filter: {}, offset: 0, status, stats, filterBar, table, body, pager } };
}

// The table of the stages of a run. Its last column has a button that opens a modal with what the stage produced (its outputs, kept for
// debugging). They are fetched when the button is clicked, for that stage only, not before.
function stagesTable(name: string, run: Run, order: string[]): HTMLTableElement {
  const table = node("table", "compact");
  headRow(table, [["Stage", false], ["Status", false], ["Started", false], ["Duration", true], ["Inferences", false], ["Error", false], ["Output", false], ["", false]]);
  const body = table.createTBody();
    // The stages the server knows, in pipeline order; then any it did not list.
  const names = [...order, ...Object.keys(run.stages).filter((s) => !order.includes(s))].filter((s) => run.stages[s]);
  for (const stage of names) {
    const s = run.stages[stage];
    const tr = body.insertRow();
    tr.insertCell().textContent = stage;
    tr.insertCell().appendChild(statusBadge(s.status));
    tr.insertCell().textContent = fmtTime(s.started_at);
    const dur = tr.insertCell();
    dur.className = "num";
    dur.textContent = fmtDuration(s.started_at, s.finished_at);
    tr.insertCell().textContent = (s.inference_ids ?? []).join(", ");
    const err = tr.insertCell();
    err.className = s.error ? "error" : "";
    err.textContent = s.error ?? "";
    const cell = tr.insertCell();
    const help = tr.insertCell();
    help.className = "help-cell";
    const text = STAGE_HELP[stage];
    if (text) {
      const q = help.appendChild(node("button", "help-btn", "?"));
      q.type = "button";
      q.title = `About ${stage}`;
      q.setAttribute("aria-label", `About ${stage}`);
      q.addEventListener("click", (ev) => {
        ev.stopPropagation();
        toggleTip(q, stage, text);
      });
    }
    if (s.status === "not_started") continue; // it never ran: nothing to show
    const btn = cell.appendChild(node("button", "small", "Outputs"));
    btn.type = "button";
    btn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      openOutputs(name, run, stage);
    });
  }
  return table;
}

// The tooltip of a "?" button: one floating box, shown under the button; clicking the button again, anywhere else, Esc or
// scrolling closes it.
let tip: { box: HTMLElement; anchor: HTMLElement } | null = null;

function closeTip(): void {
  tip?.box.remove();
  tip = null;
}

function toggleTip(anchor: HTMLElement, title: string, text: string): void {
  const same = tip?.anchor === anchor;
  closeTip();
  if (same) return;
  const box = document.body.appendChild(node("div", "stage-tip"));
  box.setAttribute("role", "tooltip");
  box.append(node("strong", "", title), node("p", "", text));
  const r = anchor.getBoundingClientRect();
  const w = Math.min(box.offsetWidth, window.innerWidth - 16);
  box.style.left = `${Math.max(8, Math.min(r.right - w, window.innerWidth - w - 8))}px`;
  const below = r.bottom + 6;
  box.style.top = `${below + box.offsetHeight > window.innerHeight ? Math.max(8, r.top - 6 - box.offsetHeight) : below}px`;
  tip = { box, anchor };
}

// JSON as DOM nodes (never as HTML), pretty-printed with 2 spaces: keys yellow (.json-key), values green (.json-val).
function jsonNodes(v: unknown, indent = ""): Node[] {
  const val = (text: string): Node => node("span", "json-val", text);
  if (v === null || v === undefined) return [val("null")];
  if (typeof v !== "object") return [val(JSON.stringify(v) ?? String(v))];
  const inner = indent + "  ";
  const isArr = Array.isArray(v);
  const entries: [string | null, unknown][] = isArr ? (v as unknown[]).map((x) => [null, x]) : Object.entries(v as Record<string, unknown>);
  const [open, close] = isArr ? ["[", "]"] : ["{", "}"];
  if (entries.length === 0) return [document.createTextNode(open + close)];
  const out: Node[] = [document.createTextNode(open + "\n")];
  entries.forEach(([k, x], i) => {
    out.push(document.createTextNode(inner));
    if (k !== null) out.push(node("span", "json-key", JSON.stringify(k)), document.createTextNode(": "));
    out.push(...jsonNodes(x, inner));
    out.push(document.createTextNode((i < entries.length - 1 ? "," : "") + "\n"));
  });
  out.push(document.createTextNode(indent + close));
  return out;
}

// Opens the modal on the outputs of `stage` of `run`; they are fetched each time it is opened.
function openOutputs(name: string, run: Run, stage: string): void {
  const modal = el<HTMLDialogElement>("rr-outputs-modal");
  const box = el("rr-outputs-body");
  el("rr-outputs-title").textContent = `Run ${run.id} · ${stage} outputs`;
  box.className = "modal-body muted";
  box.textContent = "Loading…";
  if (!modal.open) modal.showModal();
  loadOutputs(name, run, stage).then((outputs) => {
    if (modal.open && el("rr-outputs-title").textContent === `Run ${run.id} · ${stage} outputs`) showOutputs(box, outputs);
  }, (e: unknown) => {
    box.className = "modal-body error";
    box.textContent = `Failed to load the outputs: ${errText(e)}`;
  });
}

function showOutputs(box: HTMLElement, outputs: StageOutput[]): void {
  box.replaceChildren();
  if (outputs.length === 0) {
    box.className = "modal-body muted";
    box.textContent = "No outputs.";
    return;
  }
  box.className = "modal-body";
  for (const o of outputs) {
    const d = box.appendChild(node("details"));
    d.open = true;
    d.appendChild(node("summary", "", o.label ? `${o.name} · ${o.label}` : o.name));
    d.appendChild(node("pre", "ts-def json-view")).append(...jsonNodes(o.output));
  }
}

async function loadOutputs(name: string, run: Run, stage: string): Promise<StageOutput[]> {
  const res = await fetch(`api/remember-run-outputs?${new URLSearchParams({ db: name, run_id: String(run.id), stage })}`);
  if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
  return ((await res.json()) as { outputs: StageOutput[] }).outputs;
}

function detailCell(run: Run, order: string[], name: string): HTMLElement {
  const box = node("div");
  if (run.error) box.appendChild(node("p", "error", run.error));
  const props = box.appendChild(node("dl", "props"));
  const prop = (k: string, v: string): void => { props.append(node("dt", "", k), node("dd", "", v)); };
  prop("Finished", run.finished_at ? fmtTime(run.finished_at) : "—");
  box.appendChild(stagesTable(name, run, order));
  const params = box.appendChild(node("details"));
  params.appendChild(node("summary", "", "Parameters"));
  params.appendChild(node("pre", "ts-def", JSON.stringify(run.params ?? {}, null, 2)));
  return box;
}

function render(name: string, p: Panel, data: Response): void {
  const tile = (label: string, value: number, status?: string): HTMLElement => {
    const d = node("div");
    d.append(node("dt", "", label), node("dd", "", fmtInt(value)));
    if (status) {
      d.classList.add("rr-tile");
      if (p.filter.status === status) d.classList.add("selected");
      d.title = `Show only ${status} runs`;
      d.addEventListener("click", () => {
        if (p.filter.status === status) delete p.filter.status;
        else p.filter.status = status;
        p.offset = 0;
        void loadDatabase(name);
      });
    }
    return d;
  };
  p.stats.replaceChildren(tile("Runs", data.counts.total), ...STATUSES.map((s) => tile(s[0].toUpperCase() + s.slice(1), data.counts[s], s)));

  p.filterBar.replaceChildren();
  const active = (Object.entries(p.filter) as [string, string | undefined][]).filter(([, v]) => v);
  if (active.length > 0) {
    p.filterBar.appendChild(node("span", "muted", `Filtered by ${active.map(([k, v]) => `${k}: ${v}`).join(", ")}`));
    const clear = p.filterBar.appendChild(node("button", "small", "Clear filter"));
    clear.type = "button";
    clear.addEventListener("click", () => {
      p.filter = {};
      p.offset = 0;
      void loadDatabase(name);
    });
  }

  p.body.replaceChildren();
  for (const r of data.runs) {
    const tr = p.body.insertRow();
    const id = tr.insertCell();
    id.className = "num";
    id.textContent = String(r.id);
    tr.insertCell().textContent = fmtTime(r.started_at);
    tr.insertCell().textContent = r.user_id;
    tr.insertCell().textContent = r.dataset;
    tr.insertCell().textContent = r.kind;
    const st = tr.insertCell();
    st.appendChild(statusBadge(r.status));
    // A stage that failed without failing the call (web search, ...) is worth a flag.
    const failed = Object.entries(r.stages ?? {}).filter(([, s]) => s.status === "failed").map(([n]) => n);
    if (r.status !== "failed" && failed.length > 0) {
      const w = node("span", "badge badge-error", `${failed.length} stage${failed.length > 1 ? "s" : ""} failed`);
      w.title = failed.join(", ");
      st.append(" ", w);
    }
    const dur = tr.insertCell();
    dur.className = "num";
    dur.textContent = fmtDuration(r.started_at, r.finished_at);
    const art = tr.insertCell();
    art.className = "num";
    art.textContent = r.artifact_id == null ? "" : String(r.artifact_id);

    const detail = p.body.insertRow();
    detail.hidden = true;
    detail.className = "detail";
    const cell = detail.insertCell();
    cell.colSpan = tr.cells.length;
    tr.addEventListener("click", () => {
      if (!cell.firstChild) cell.appendChild(detailCell(r, data.stages, name));
      detail.hidden = !detail.hidden;
      tr.classList.toggle("selected", !detail.hidden);
    });
  }
  p.table.hidden = data.runs.length === 0;
  p.status.className = "muted";
  p.status.textContent = data.total === 0 ? "No remember runs." :
    `Showing ${data.offset + 1}–${data.offset + data.runs.length} of ${fmtInt(data.total)} runs.`;

  p.pager.replaceChildren();
  const page = (label: string, offset: number, enabled: boolean): void => {
    const b = p.pager.appendChild(node("button", "small", label));
    b.type = "button";
    b.disabled = !enabled;
    b.addEventListener("click", () => {
      p.offset = offset;
      void loadDatabase(name);
    });
  };
  if (data.total > data.limit) {
    page("← Newer", Math.max(0, data.offset - data.limit), data.offset > 0);
    page("Older →", data.offset + data.limit, data.offset + data.limit < data.total);
  }
}

async function loadDatabase(name: string): Promise<void> {
  const p = panels.get(name);
  if (!p) return;
  p.status.className = "muted";
  p.status.textContent = "Loading…";
  const params = new URLSearchParams({ db: name, limit: String(PAGE), offset: String(p.offset) });
  for (const [k, v] of Object.entries(p.filter)) if (v) params.set(k, v);
  try {
    const res = await fetch(`api/remember-runs?${params}`);
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    render(name, p, (await res.json()) as Response);
  } catch (err) {
    p.status.className = "error";
    p.status.textContent = `Failed to load remember runs: ${errText(err)}`;
  }
}

async function load(): Promise<void> {
  const root = el("remember_runs-content");
  if (panels.size === 0) {
    try {
      const res = await fetch("api/databases");
      if (!res.ok) throw new Error(`${res.status}`);
      const names = ((await res.json()) as { databases: string[] }).databases;
      for (const name of names) {
        const { box, panel } = makePanel(name);
        panels.set(name, panel);
        root.appendChild(box);
      }
    } catch (err) {
      root.textContent = `Failed to list databases: ${errText(err)}`;
      return;
    }
  }
  await Promise.all([...panels.keys()].map((n) => loadDatabase(n)));
}

export function initRememberRuns(): void {
  document.addEventListener("click", (e) => { if (tip && !tip.box.contains(e.target as Node)) closeTip(); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeTip(); });
  window.addEventListener("scroll", closeTip, true);
  const modal = el<HTMLDialogElement>("rr-outputs-modal");
  el("rr-outputs-close").addEventListener("click", () => modal.close());
  el("remember_runs-refresh").addEventListener("click", () => void load());
  void load();
}
