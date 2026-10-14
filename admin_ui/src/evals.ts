// Evals block of the admin UI: experiments (counts + listing), their runs, a run's evals with their records, and
// the ATIF trajectories of each eval (opened as a flow graph by trace.ts). Talks to /admin/api/evals/*.

import { showGraphAt } from "./graph.js";
import { showEvalHistory } from "./history.js";
import { showTrace } from "./trace.js";
import { showWebSearches, totalsLine, type WebSearch, type WebSearchTotals } from "./websearch.js";

interface Run {
  id: number; experiment_id: number; status: string; params?: Record<string, unknown>;
  total_evals: number; passed_evals: number; failed_evals: number; cost_usd: number;
  started_at: string; finished_at?: string; evals?: RunEval[];
}
interface Trajectory {
  seq: number; kind: string; trajectory_id: string; agent_name: string; steps: number; cost_usd: number;
  artifacts?: Artifact[];
}
interface Artifact { id: number; kind: string; text: string; valid_at: string; created_at: string }
interface RunEval {
  id: number; suite: string; name: string; status: string; answer?: string; problems?: string[]; passed_checks?: string[]; dataset_id?: number; error?: string;
  steps: number; cost_usd: number; started_at: string; finished_at: string; trajectories?: Trajectory[];
}
// The web searches the remember calls of one eval made (see websearch.ts), read from its trajectories.
interface EvalWebSearches { run_eval_id: number; totals: WebSearchTotals; searches: WebSearch[] }
interface ExperimentRow {
  id: number; name: string; description?: string; created_at: string;
  runs: number; evals: number; runs_passed: number; runs_failed: number; cost_usd: number; last_run?: Run;
}
interface Overview {
  totals: { experiments: number; runs: number; evals: number; executions: number; passed: number; failed: number; cost_usd: number };
  experiments: ExperimentRow[];
}
interface ExperimentDetail {
  id: number; name: string; description?: string; config?: Record<string, unknown>;
  evals?: { suite: string; name: string }[]; runs: Run[];
}

function $<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

// Creates an element with optional class and text (textContent only: never inject server data as HTML).
function h<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
  return (await res.json()) as T;
}

const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));

function fmtTime(t: string | undefined): string {
  if (!t) return "–";
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? t : d.toISOString().replace("T", " ").slice(0, 19);
}

function fmtDuration(from: string | undefined, to: string | undefined): string {
  if (!from || !to) return "–";
  const ms = new Date(to).getTime() - new Date(from).getTime();
  if (Number.isNaN(ms) || ms < 0) return "–";
  if (ms < 1000) return `${ms} ms`;
  const s = ms / 1000;
  return s < 60 ? `${s.toFixed(1)} s` : `${Math.floor(s / 60)} min ${Math.round(s % 60)} s`;
}

const fmtCost = (usd: number): string => `$${usd.toFixed(4)}`;

function statusBadge(status: string): HTMLElement {
  const cls = status === "passed" ? "badge-ok" : status === "running" ? "badge-unknown" : "badge-error";
  return h("span", `badge ${cls}`, status);
}

function numCell(row: HTMLTableRowElement, text: string): void {
  row.insertCell().textContent = text;
  row.cells[row.cells.length - 1].className = "num";
}

function table(columns: [string, boolean][]): { table: HTMLTableElement; body: HTMLTableSectionElement } {
  const t = h("table");
  const head = t.createTHead().insertRow();
  for (const [label, num] of columns) head.appendChild(h("th", num ? "num" : undefined, label));
  return { table: t, body: t.createTBody() };
}

function stat(label: string, value: string): HTMLElement {
  const d = h("div");
  d.append(h("dt", undefined, label), h("dd", undefined, value));
  return d;
}

function props(rows: [string, string | undefined][]): HTMLElement {
  const dl = h("dl", "props");
  for (const [k, v] of rows) {
    if (!v) continue;
    dl.append(h("dt", undefined, k), h("dd", undefined, v));
  }
  return dl;
}

//---------------------------------------------------------------------------------------------

let selectedExperiment: number | undefined;

async function loadOverview(): Promise<void> {
  const status = $("evals-status");
  const stats = $("evals-stats");
  const body = $("evals-experiments");
  status.className = "muted";
  status.textContent = "Loading…";
  try {
    const data = await getJSON<Overview>("api/evals");
    const t = data.totals;
    stats.replaceChildren(
      stat("Experiments", String(t.experiments)), stat("Runs", String(t.runs)), stat("Distinct evals", String(t.evals)),
      stat("Eval results", t.executions ? `${t.passed}/${t.executions} passed` : "–"), stat("Cost", fmtCost(t.cost_usd)),
    );
    const list = data.experiments ?? [];
    status.textContent = list.length === 0 ? "No experiments yet. Run ./run_evals.sh to record one." : "Click an experiment to list its runs.";
    const { table: tbl, body: tbody } = table([
      ["Experiment", false], ["Description", false], ["Runs", true], ["Passed", true], ["Failed", true], ["Evals", true],
      ["Cost", true], ["Last run", false],
    ]);
    tbl.className = "clickable";
    for (const e of list) {
      const tr = tbody.insertRow();
      tr.classList.toggle("selected", e.id === selectedExperiment);
      tr.insertCell().textContent = e.name;
      tr.insertCell().textContent = e.description ?? "";
      numCell(tr, String(e.runs));
      numCell(tr, String(e.runs_passed));
      numCell(tr, String(e.runs_failed));
      numCell(tr, String(e.evals));
      numCell(tr, fmtCost(e.cost_usd));
      const last = tr.insertCell();
      if (e.last_run) last.append(statusBadge(e.last_run.status), ` ${fmtTime(e.last_run.started_at)}`);
      else last.textContent = "–";
      tr.addEventListener("click", () => {
        selectedExperiment = e.id;
        tbody.querySelectorAll("tr").forEach((r) => r.classList.toggle("selected", r === tr));
        void loadExperiment(e.id);
      });
    }
    body.replaceChildren(...(list.length ? [tbl] : []));
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load experiments: ${errText(err)}`;
  }
}

async function loadExperiment(id: number): Promise<void> {
  const box = $("evals-runs");
  box.replaceChildren(h("p", "muted", "Loading…"));
  try {
    const exp = await getJSON<ExperimentDetail>(`api/evals/experiments/${id}`);
    const head = h("h3", undefined, `${exp.name} — ${exp.runs.length} run${exp.runs.length === 1 ? "" : "s"}`);
    const extra: HTMLElement[] = [];
    if (exp.description) extra.push(h("p", "muted", exp.description));
    if (exp.config && Object.keys(exp.config).length) extra.push(props([["config", JSON.stringify(exp.config)]]));
    if (exp.evals?.length) {
      const ul = h("ul", "eval-list");
      for (const e of exp.evals) {
        const li = h("li");
        const btn = h("button", "small", "History");
        btn.type = "button";
        btn.addEventListener("click", () => void showEvalHistory(exp.id, exp.name, e.suite, e.name, (id) => void showRun(id, exp.name)));
        li.append(btn, " ", h("code", undefined, `${e.suite}/${e.name}`));
        ul.append(li);
      }
      extra.push(h("h4", undefined, "Evals"), ul);
    }

    const { table: tbl, body } = table([
      ["Run", false], ["Status", false], ["Passed", true], ["Failed", true], ["Total", true], ["Cost", true],
      ["Started (UTC)", false], ["Duration", true],
    ]);
    tbl.className = "clickable";
    for (const r of exp.runs) {
      const tr = body.insertRow();
      tr.insertCell().textContent = `#${r.id}`;
      tr.insertCell().append(statusBadge(r.status));
      numCell(tr, String(r.passed_evals));
      numCell(tr, String(r.failed_evals));
      numCell(tr, String(r.total_evals));
      numCell(tr, fmtCost(r.cost_usd));
      tr.insertCell().textContent = fmtTime(r.started_at);
      numCell(tr, fmtDuration(r.started_at, r.finished_at));
      tr.addEventListener("click", () => void showRun(r.id, exp.name));
    }
    box.replaceChildren(head, ...extra, exp.runs.length ? tbl : h("p", "muted", "No runs."));
  } catch (err) {
    box.replaceChildren(h("p", "error", `Failed to load runs: ${errText(err)}`));
  }
}

//---------------------------------------------------------------------------------------------

// Opens the run modal: the run's evals; clicking an eval shows its record (answer, problems, trajectories).
async function showRun(runID: number, experiment: string): Promise<void> {
  const modal = $<HTMLDialogElement>("run-modal");
  const status = $("run-status");
  const content = $("run-content");
  $("run-title").textContent = `${experiment} — run #${runID}`;
  status.className = "muted";
  status.textContent = "Loading…";
  content.replaceChildren();
  if (!modal.open) modal.showModal();
  try {
    const [run, webSearches] = await Promise.all([
      getJSON<Run>(`api/evals/runs/${runID}`),
      // Informational: the run is shown without them if they cannot be read.
      getJSON<{ evals: EvalWebSearches[] }>(`api/evals/runs/${runID}/web-searches`).then((r) => r.evals ?? [], () => [] as EvalWebSearches[]),
    ]);
    const webByEval = new Map(webSearches.map((w) => [w.run_eval_id, w]));
    const evals = run.evals ?? [];
    status.replaceChildren(statusBadge(run.status), ` ${run.passed_evals}/${run.total_evals} passed · ${fmtCost(run.cost_usd)} · ${fmtDuration(run.started_at, run.finished_at)}`);
    content.append(props([
      ["started", `${fmtTime(run.started_at)} UTC`], ["finished", run.finished_at ? `${fmtTime(run.finished_at)} UTC` : undefined],
      ["params", run.params && Object.keys(run.params).length ? JSON.stringify(run.params) : undefined],
    ]));
    if (evals.length === 0) {
      content.append(h("p", "muted", "No evals recorded in this run."));
      return;
    }
    const { table: tbl, body } = table([
      ["Suite", false], ["Eval", false], ["Status", false], ["Steps", true], ["Cost", true], ["Duration", true], ["Trajectories", true], ["Web searches", true],
    ]);
    tbl.className = "clickable";
    for (const e of evals) {
      const tr = body.insertRow();
      tr.insertCell().textContent = e.suite;
      tr.insertCell().textContent = e.name;
      tr.insertCell().append(statusBadge(e.status));
      numCell(tr, String(e.steps));
      numCell(tr, fmtCost(e.cost_usd));
      numCell(tr, fmtDuration(e.started_at, e.finished_at));
      numCell(tr, String(e.trajectories?.length ?? 0));
      numCell(tr, webByEval.has(e.id) ? String(webByEval.get(e.id)!.searches.length) : "–");
      const detail = body.insertRow();
      detail.hidden = true;
      detail.className = "detail";
      const cell = detail.insertCell();
      cell.colSpan = 8;
      cell.append(...evalRecord(run, e, experiment, webByEval.get(e.id)));
      tr.addEventListener("click", () => {
        detail.hidden = !detail.hidden;
        tr.classList.toggle("selected", !detail.hidden);
      });
    }
    content.append(tbl);
    // A single-eval run is shown expanded.
    if (evals.length === 1) body.rows[0].click();
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load run: ${errText(err)}`;
  }
}

// The detail of one eval: its result and the list of its ATIF trajectories, each opening the flow graph.
function evalRecord(run: Run, e: RunEval, experiment: string, web?: EvalWebSearches): HTMLElement[] {
  const out: HTMLElement[] = [
    props([
      ["started", `${fmtTime(e.started_at)} UTC`], ["answer", e.answer], ["error", e.error],
    ]),
  ];
  const passed = e.passed_checks ?? [];
  const failed = e.problems ?? [];
  if (passed.length || failed.length) {
    out.push(h("h4", undefined, `Checks (${passed.length} passed, ${failed.length} failed)`));
    const ul = h("ul", "checks");
    for (const p of passed) ul.append(h("li", "pass", p));
    for (const p of failed) ul.append(h("li", "fail", p));
    out.push(ul);
  }
  const hbtn = h("button", "small", "History");
  hbtn.type = "button";
  hbtn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    void showEvalHistory(run.experiment_id, experiment, e.suite, e.name, (id) => void showRun(id, experiment));
  });
  out.push(h("h4", undefined, "History"), hbtn);
  out.push(h("h4", undefined, "Knowledge graph"));
  if (e.dataset_id != null) {
    const gbtn = h("button", "small", "View graph");
    gbtn.type = "button";
    gbtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      void showGraphAt(`${e.suite}/${e.name} (run #${run.id})`, `api/evals/run-evals/${e.id}/graph`);
    });
    out.push(gbtn);
  } else {
    out.push(h("p", "muted", "No dataset recorded."));
  }
  if (web) {
    out.push(h("h4", undefined, "Web searches"), h("p", "muted", totalsLine(web.totals)));
    const wbtn = h("button", "small", "View web searches");
    wbtn.type = "button";
    wbtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      showWebSearches(`${e.suite}/${e.name} (run #${run.id})`, web.searches, totalsLine(web.totals));
    });
    out.push(wbtn);
  }
  out.push(h("h4", undefined, "ATIF traces"));
  const trajs = e.trajectories ?? [];
  if (trajs.length === 0) {
    out.push(h("p", "muted", "No trajectories recorded."));
    return out;
  }
  const list = h("ul", "traces");
  for (const t of trajs) {
    const li = h("li");
    const btn = h("button", "small", "View trace");
    btn.type = "button";
    btn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      void showTrace(t.trajectory_id, `${e.suite}/${e.name} · #${t.seq} ${t.kind} (run #${run.id})`);
    });
    const artBtn = h("button", "small", "View artifacts");
    artBtn.type = "button";
    let panel: HTMLElement | undefined;
    artBtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      if (!panel) {
        panel = artifactsPanel(t);
        li.append(panel);
      } else {
        panel.hidden = !panel.hidden;
      }
      artBtn.textContent = panel.hidden ? "View artifacts" : "Hide artifacts";
    });
    li.append(
      btn, " ", artBtn, " ", h("strong", undefined, `#${t.seq} ${t.kind}`),
      h("span", "muted", ` ${t.steps} step${t.steps === 1 ? "" : "s"} · ${fmtCost(t.cost_usd)} · `),
      h("code", undefined, t.trajectory_id),
    );
    list.append(li);
  }
  out.push(list);
  return out;
}

// The artifacts of one trajectory: for a remember call the input artifact the caller gave, for a recall the artifacts
// the answer was sourced from (the provenance of its evidence).
function artifactsPanel(t: Trajectory): HTMLElement {
  const box = h("div", "artifacts");
  const arts = t.artifacts ?? [];
  box.append(h("h4", undefined, t.kind === "remember" ? "Input artifact" : "Source artifacts (provenance of the answer)"));
  if (arts.length === 0) {
    box.append(h("p", "muted", t.kind === "remember" ? "No input artifact recorded." : "The answer cites no artifacts."));
    return box;
  }
  for (const a of arts) {
    const item = h("div", "artifact");
    item.append(
      h("strong", undefined, `artifact #${a.id}`),
      h("span", "muted", ` ${a.kind} · valid ${fmtTime(a.valid_at)} · ingested ${fmtTime(a.created_at)}`),
      h("pre", undefined, a.text),
    );
    box.append(item);
  }
  return box;
}

export function initEvals(): void {
  const modal = $<HTMLDialogElement>("run-modal");
  $("run-close").addEventListener("click", () => modal.close());
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
  $("evals-refresh").addEventListener("click", () => {
    void loadOverview();
    if (selectedExperiment !== undefined) void loadExperiment(selectedExperiment);
  });
  void loadOverview();
}
