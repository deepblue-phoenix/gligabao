// The "remember runs" view (see docs/remember.md#run-log): per database, how many runs of the remember pipeline are in
// each status and the list of the runs, newest first. Clicking a status tile filters the list by it; clicking a run shows
// its parameters and how each stage went (status, error, time, model runs).

interface Stage {
  status: string; error: string | null; started_at: string | null; finished_at: string | null; model_run_ids: number[];
}
interface Run {
  id: number; user_id: string; dataset: string; kind: string; params: Record<string, unknown>; artifact_id: number | null;
  status: string; error: string | null; started_at: string; finished_at: string | null; stages: Record<string, Stage>;
}
interface Counts { total: number; started: number; succeeded: number; failed: number }
interface Response { counts: Counts; stages: string[]; runs: Run[]; total: number; limit: number; offset: number }
interface Filter { status?: string; dataset?: string }

const PAGE = 100;
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

function stagesTable(run: Run, order: string[]): HTMLTableElement {
  const table = node("table", "compact");
  headRow(table, [["Stage", false], ["Status", false], ["Started", false], ["Duration", true], ["Model runs", false], ["Error", false]]);
  const body = table.createTBody();
  // The stages the server knows, in pipeline order; then any it did not list.
  const names = [...order, ...Object.keys(run.stages).filter((s) => !order.includes(s))].filter((s) => run.stages[s]);
  for (const name of names) {
    const s = run.stages[name];
    const tr = body.insertRow();
    tr.insertCell().textContent = name;
    tr.insertCell().appendChild(statusBadge(s.status));
    tr.insertCell().textContent = fmtTime(s.started_at);
    const dur = tr.insertCell();
    dur.className = "num";
    dur.textContent = fmtDuration(s.started_at, s.finished_at);
    tr.insertCell().textContent = (s.model_run_ids ?? []).join(", ");
    const err = tr.insertCell();
    err.className = s.error ? "error" : "";
    err.textContent = s.error ?? "";
  }
  return table;
}

function detailCell(run: Run, order: string[], p: Panel, name: string): HTMLElement {
  const box = node("div");
  if (run.error) box.appendChild(node("p", "error", run.error));
  const props = box.appendChild(node("dl", "props"));
  const prop = (k: string, v: string): void => { props.append(node("dt", "", k), node("dd", "", v)); };
  prop("Finished", run.finished_at ? fmtTime(run.finished_at) : "—");
  const dsBtn = node("button", "small", "Only this dataset");
  dsBtn.type = "button";
  dsBtn.addEventListener("click", () => {
    p.filter.dataset = run.dataset;
    p.offset = 0;
    void loadDatabase(name);
  });
  box.appendChild(stagesTable(run, order));
  const params = box.appendChild(node("details"));
  params.appendChild(node("summary", "", "Parameters"));
  params.appendChild(node("pre", "ts-def", JSON.stringify(run.params ?? {}, null, 2)));
  box.appendChild(dsBtn);
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
      if (!cell.firstChild) cell.appendChild(detailCell(r, data.stages, p, name));
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
  const root = el("rememberruns-content");
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
  el("rememberruns-refresh").addEventListener("click", () => void load());
  void load();
}
