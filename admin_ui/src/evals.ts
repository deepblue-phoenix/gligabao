// Evals block of the admin UI: experiments (counts + listing), their runs, a run's results with their records, and
// the ATIF trajectories of each case (opened as a flow graph by trace.ts). Talks to /admin/api/evals/*.

import { bookmarkButton, registerBookmarkOpener, type Bookmark } from "./bookmarks.js";
import { showGraphAt } from "./graph.js";
import { showCaseHistory } from "./history.js";
import { openTemporalAt } from "./temporal.js";
import { showTrace } from "./trace.js";
import { showWebSearches, totalsLine, type WebSearch, type WebSearchTotals } from "./websearch.js";

// The words of the model (internal/evals/core): Status is the lifecycle of a run or case run, Verdict the judgement of a
// completed case run (empty when it has none). They are shown as they come.
type Status = "pending" | "running" | "completed" | "errored" | "cancelled";
type Verdict = "pass" | "fail";
// core.Counts: every case run is in exactly one of pending, running, completed, errored, cancelled; passed and failed split the
// completed ones that have a verdict.
interface Counts { total: number; pending: number; running: number; completed: number; errored: number; cancelled: number; passed: number; failed: number }
interface Run {
  id: number; experiment_id: number; status: Status; error?: string; params?: Record<string, unknown>;
  counts: Counts; cost_usd: number; started_at: string; finished_at?: string; case_runs?: CaseRun[];
}
interface Trajectory {
  seq: number; kind: string; trajectory_id: string; agent_name: string; steps: number; cost_usd: number;
  artifacts?: Artifact[];
}
interface Artifact { id: number; kind: string; text: string; valid_at: string; created_at: string }
// Says of a case run that it is a rerun with other models (empty model: the stages kept the models they had).
interface RerunInfo { of: number; remember_model?: string; query_model?: string }
// The outcome of one case of an eval in one run.
interface CaseRun {
  id: number; eval: string; name: string; status: Status; verdict?: Verdict; score?: number; answer?: string; problems?: string[]; passed_checks?: string[]; dataset_id?: number; error?: string;
  steps: number; cost_usd: number; started_at: string; finished_at: string; trajectories?: Trajectory[]; rerun?: RerunInfo;
}
// The web searches the remember calls of one result (case) made (see websearch.ts), read from its trajectories.
interface EvalWebSearches { case_run_id: number; totals: WebSearchTotals; searches: WebSearch[] }
// An LLM commentary on a failed case run (core.CaseCommentary).
interface CaseCommentary {
  id: number; model: string; text: string; input_tokens: number; output_tokens: number; cost_usd: number; created_at: string;
}
// What GET api/evals/case_runs/{id}/debug returns: everything about one case run in one place.
interface CaseDebug {
  case_run_id: number; eval: string; name: string; status: Status; verdict?: Verdict; error?: string; answer?: string;
  problems: string[]; passed_checks: string[];
  input: { remember?: { kind?: string; text?: string; text_file?: string }[]; query?: string };
  expected?: Record<string, unknown>; params?: Record<string, unknown>; dataset_id?: number;
  commentable: boolean; commentary_enabled: boolean; default_model: string; commentaries: CaseCommentary[];
  cost_usd: number; started_at: string; finished_at?: string;
  // Set when this case run is itself a rerun; rerun_options when the server can rerun the case (the models a rerun can pick).
  rerun?: RerunInfo; rerun_options?: { models: string[] };
}
// What POST api/evals/case_runs/{id}/rerun answers: the new case run, already recorded (running).
interface RerunStarted { case_run_id: number; eval_run_id: number; experiment_run_id?: number }
interface ExperimentRow {
  id: number; name: string; description?: string; created_at: string;
  runs: number; cases: number; runs_by_status: Partial<Record<Status, number>>; cost_usd: number; last_run?: Run;
}
interface Overview {
  totals: { experiments: number; runs: number; cases: number; counts: Counts; cost_usd: number };
  experiments: ExperimentRow[];
}
interface ExperimentDetail {
  id: number; name: string; description?: string; config?: Record<string, unknown>;
  cases?: { eval: string; name: string }[]; runs: Run[];
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

// The badge of a status as the model has it: completed is ok, errored an error, running and pending are active, cancelled is muted.
function statusBadge(status: string): HTMLElement {
  const cls = status === "completed" ? "badge-ok" : status === "errored" ? "badge-error" : status === "cancelled" ? "badge-unknown" : "badge-active";
  return h("span", `badge ${cls}`, status);
}

// The badge of a verdict (pass or fail); "–" when the case run has none.
function verdictBadge(verdict: string | undefined): HTMLElement {
  if (!verdict) return h("span", "muted", "–");
  return h("span", `badge ${verdict === "pass" ? "badge-ok" : "badge-error"}`, verdict);
}

// The counts of a run in the model's words, leaving out the zeros: "3 pass · 1 fail · 1 errored of 5".
function countsLine(c: Counts): string {
  const parts: [number, string][] = [
    [c.passed, "pass"], [c.failed, "fail"], [c.completed - c.passed - c.failed, "no verdict"], [c.errored, "errored"], [c.cancelled, "cancelled"],
    [c.running, "running"], [c.pending, "pending"],
  ];
  const shown = parts.filter(([n]) => n > 0).map(([n, w]) => `${n} ${w}`);
  return `${shown.length ? shown.join(" · ") : "no case runs"} of ${c.total}`;
}

// The statuses of the runs of an experiment as "3 completed · 1 errored".
function statusLine(by: Partial<Record<Status, number>>): string {
  const parts = Object.entries(by).filter(([, n]) => (n ?? 0) > 0).map(([s, n]) => `${n} ${s}`);
  return parts.length ? parts.join(" · ") : "–";
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
      stat("Experiments", String(t.experiments)), stat("Runs", String(t.runs)), stat("Distinct cases", String(t.cases)),
      stat("Case runs", t.counts.total ? countsLine(t.counts) : "–"), stat("Cost", fmtCost(t.cost_usd)),
    );
    const list = data.experiments ?? [];
    status.textContent = list.length === 0 ? "No experiments yet. Run ./run_evals.sh to record one." : "Click an experiment to list its runs.";
    const { table: tbl, body: tbody } = table([
      ["Experiment", false], ["Description", false], ["Runs", true], ["Run statuses", false], ["Cases", true],
      ["Cost", true], ["Last run", false],
    ]);
    tbl.className = "clickable";
    for (const e of list) {
      const tr = tbody.insertRow();
      tr.classList.toggle("selected", e.id === selectedExperiment);
      tr.insertCell().textContent = e.name;
      tr.insertCell().textContent = e.description ?? "";
      numCell(tr, String(e.runs));
      tr.insertCell().textContent = statusLine(e.runs_by_status ?? {});
      numCell(tr, String(e.cases));
      numCell(tr, fmtCost(e.cost_usd));
      const last = tr.insertCell();
      if (e.last_run) last.append(statusBadge(e.last_run.status), ` ${countsLine(e.last_run.counts)} · ${fmtTime(e.last_run.started_at)}`);
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
    if (exp.cases?.length) {
      const ul = h("ul", "case-list");
      for (const e of exp.cases) {
        const li = h("li");
        const btn = h("button", "small", "History");
        btn.type = "button";
        btn.addEventListener("click", () => void showCaseHistory(exp.id, exp.name, e.eval, e.name, (id) => void showRun(id, exp.name)));
        li.append(btn, " ", h("code", undefined, `${e.eval}/${e.name}`));
        ul.append(li);
      }
      extra.push(h("h4", undefined, "Cases"), ul);
    }

    const { table: tbl, body } = table([
      ["Run", false], ["Status", false], ["Case runs", false], ["Cost", true], ["Started (UTC)", false], ["Duration", true],
    ]);
    tbl.className = "clickable";
    for (const r of exp.runs) {
      const tr = body.insertRow();
      tr.insertCell().textContent = `#${r.id}`;
      tr.insertCell().append(statusBadge(r.status));
      tr.insertCell().textContent = countsLine(r.counts);
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

// Opens the run modal: the run's results; clicking one shows its record (answer, problems, trajectories).
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
      getJSON<{ case_runs: EvalWebSearches[] }>(`api/evals/runs/${runID}/web-searches`).then((r) => r.case_runs ?? [], () => [] as EvalWebSearches[]),
    ]);
    const webByCase = new Map(webSearches.map((w) => [w.case_run_id, w]));
    const cases = run.case_runs ?? [];
    status.replaceChildren(statusBadge(run.status), ` ${countsLine(run.counts)} · ${fmtCost(run.cost_usd)} · ${fmtDuration(run.started_at, run.finished_at)}`);
    content.append(props([
      ["started", `${fmtTime(run.started_at)} UTC`], ["finished", run.finished_at ? `${fmtTime(run.finished_at)} UTC` : undefined],
      ["error", run.error],
      ["params", run.params && Object.keys(run.params).length ? JSON.stringify(run.params) : undefined],
    ]));
    if (cases.length === 0) {
      content.append(h("p", "muted", "No cases recorded in this run."));
      return;
    }
    const { table: tbl, body } = table([
      ["Eval", false], ["Case", false], ["Status", false], ["Verdict", false], ["Steps", true], ["Cost", true], ["Duration", true], ["Trajectories", true], ["Web searches", true],
    ]);
    tbl.className = "clickable";
    for (const e of cases) {
      const tr = body.insertRow();
      tr.insertCell().textContent = e.eval;
      const nameCell = tr.insertCell();
      nameCell.textContent = e.name;
      if (e.rerun) nameCell.append(" ", h("span", "badge badge-active", "rerun"), h("span", "muted", ` of #${e.rerun.of}${rerunModels(e.rerun)}`));
      tr.insertCell().append(statusBadge(e.status));
      tr.insertCell().append(verdictBadge(e.verdict));
      numCell(tr, String(e.steps));
      numCell(tr, fmtCost(e.cost_usd));
      numCell(tr, fmtDuration(e.started_at, e.finished_at));
      numCell(tr, String(e.trajectories?.length ?? 0));
      numCell(tr, webByCase.has(e.id) ? String(webByCase.get(e.id)!.searches.length) : "–");
      const detail = body.insertRow();
      detail.hidden = true;
      detail.className = "detail";
      const cell = detail.insertCell();
      cell.colSpan = 9;
      cell.append(...caseRecord(run, e, experiment, webByCase.get(e.id)));
      tr.addEventListener("click", () => {
        detail.hidden = !detail.hidden;
        tr.classList.toggle("selected", !detail.hidden);
      });
    }
    content.append(tbl);
    // A single-case run is shown expanded.
    if (cases.length === 1) body.rows[0].click();
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load run: ${errText(err)}`;
  }
}

// The detail of one case: its result and the list of its ATIF trajectories, each opening the flow graph.
function caseRecord(run: Run, e: CaseRun, experiment: string, web?: EvalWebSearches): HTMLElement[] {
  const out: HTMLElement[] = [
    props([
      ["status", e.status], ["verdict", e.verdict], ["score", e.score != null ? String(e.score) : undefined],
      ["started", `${fmtTime(e.started_at)} UTC`], ["answer", e.answer], ["error", e.error],
    ]),
  ];
  // Everything about the case (checks, graph, temporal plot, traces) next to the LLM's commentary on its failure.
  const cbtn = h("button", "small", e.verdict === "fail" || e.status === "errored" ? "Debug & commentary" : "Case details");
  cbtn.type = "button";
  cbtn.title = "Open the checks that failed and held, the graph and temporal plot of the case, and the LLM's commentary on a failure";
  cbtn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    void showCase(run, e, experiment);
  });
  out.push(cbtn, caseBookmark(run, e, experiment));
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
    void showCaseHistory(run.experiment_id, experiment, e.eval, e.name, (id) => void showRun(id, experiment));
  });
  out.push(h("h4", undefined, "History"), hbtn);
  out.push(h("h4", undefined, "Knowledge graph"));
  if (e.dataset_id != null) {
    const gbtn = h("button", "small", "View graph");
    gbtn.type = "button";
    gbtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      void showGraphAt(`${e.eval}/${e.name} (run #${run.id})`, `api/evals/case_runs/${e.id}/graph`);
    });
    out.push(gbtn);
    // What the remember calls of the case did to the facts over time (created, confirmed, ended, invalidated, superseded).
    out.push(h("h4", undefined, "Temporal changes"));
    const tbtn = h("button", "small", "View temporal changes");
    tbtn.type = "button";
    tbtn.title = "Plot what the remember calls of this case did to the facts on a time axis: established, invalidated, still true";
    tbtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      openTemporalAt(`${e.eval}/${e.name} (run #${run.id}) · temporal changes`, `api/evals/case_runs/${e.id}/temporal`);
    });
    out.push(tbtn);
  } else {
    out.push(h("p", "muted", "No dataset recorded."));
  }
  if (web) {
    out.push(h("h4", undefined, "Web searches"), h("p", "muted", totalsLine(web.totals)));
    const wbtn = h("button", "small", "View web searches");
    wbtn.type = "button";
    wbtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      showWebSearches(`${e.eval}/${e.name} (run #${run.id})`, web.searches, totalsLine(web.totals));
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
      void showTrace(t.trajectory_id, `${e.eval}/${e.name} · #${t.seq} ${t.kind} (run #${run.id})`);
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

//---------------------------------------------------------------------------------------------

// Opens the case modal of one case run: on the left the checks that failed and the ones that held (and the case as it ran), on the
// right the LLM's commentary and recommendation, which can be asked for when the case failed. The header has the buttons of the
// knowledge graph and the temporal plot the case built, so the failure can be debugged against everything it ran on.
async function showCase(run: Run, e: CaseRun, experiment: string): Promise<void> {
  const modal = $<HTMLDialogElement>("case-modal");
  const status = $("case-status");
  const actions = $("case-actions");
  const left = $("case-left");
  const right = $("case-right");
  const title = `${e.eval}/${e.name} (run #${run.id})`;
  $("case-title").textContent = title;
  status.className = "muted";
  status.textContent = "Loading…";
  actions.replaceChildren(caseBookmark(run, e, experiment));
  left.replaceChildren();
  right.replaceChildren();
  if (!modal.open) modal.showModal();
  try {
    const d = await getJSON<CaseDebug>(`api/evals/case_runs/${e.id}/debug`);
    if (!modal.open) return; // closed while loading
    status.replaceChildren(statusBadge(d.status), " ", verdictBadge(d.verdict));
    if (d.dataset_id != null) {
      const gbtn = h("button", "small", "Knowledge graph");
      gbtn.type = "button";
      gbtn.title = "The knowledge graph this case built";
      gbtn.addEventListener("click", () => void showGraphAt(title, `api/evals/case_runs/${e.id}/graph`));
      const tbtn = h("button", "small", "Temporal plot");
      tbtn.type = "button";
      tbtn.title = "What the remember calls of this case did to the facts over time";
      tbtn.addEventListener("click", () => openTemporalAt(`${title} · temporal changes`, `api/evals/case_runs/${e.id}/temporal`));
      actions.append(gbtn, tbtn);
    }
    left.append(...caseLeft(d, run, e));
    right.append(...caseRight(d, run, experiment));
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load the case: ${errText(err)}`;
  }
}

// The bookmark button of one case run: what it opens again is the case modal of that run's result.
function caseBookmark(run: Run, e: CaseRun, experiment: string): HTMLButtonElement {
  return bookmarkButton({
    id: `eval_case:${e.id}`,
    kind: "eval_case",
    title: `${e.eval}/${e.name}`,
    detail: `${experiment} · run #${run.id}`,
    ref: { case_run_id: e.id, run_id: run.id, experiment },
  });
}

// Opens a bookmarked case: the run it is in (behind), and on top the case modal.
async function openBookmarkedCase(b: Bookmark): Promise<void> {
  const run = await getJSON<Run>(`api/evals/runs/${Number(b.ref.run_id)}`);
  const e = (run.case_runs ?? []).find((c) => c.id === Number(b.ref.case_run_id));
  if (!e) throw new Error("the case run is not in its run any more");
  const experiment = String(b.ref.experiment);
  void showRun(run.id, experiment);
  await showCase(run, e, experiment);
}

// The left pane: the failure, the checks that failed and the ones that held, the case as it ran and as it is defined, the traces.
function caseLeft(d: CaseDebug, run: Run, e: CaseRun): HTMLElement[] {
  const out: HTMLElement[] = [h("h3", undefined, `Checks (${d.problems.length} failed, ${d.passed_checks.length} held)`)];
  if (d.error) out.push(h("p", "error", `The case could not run to the end: ${d.error}`));
  const checks = (cls: "pass" | "fail", list: string[]): HTMLElement => {
    const ul = h("ul", "checks");
    for (const c of list) ul.append(h("li", cls, c));
    return ul;
  };
  out.push(h("h4", undefined, `Failed (${d.problems.length})`));
  out.push(d.problems.length ? checks("fail", d.problems) : h("p", "muted", d.error ? "No check ran." : "None failed."));
  out.push(h("h4", undefined, `Held (${d.passed_checks.length})`));
  out.push(d.passed_checks.length ? checks("pass", d.passed_checks) : h("p", "muted", "None held."));
  if (d.answer) out.push(h("h4", undefined, "Answer given"), h("pre", undefined, d.answer));

  const input = d.input ?? {};
  const given = h("details");
  given.append(h("summary", undefined, "What the case was given (as it ran)"));
  if (input.query) given.append(h("h4", undefined, "Question"), h("pre", undefined, input.query));
  (input.remember ?? []).forEach((r, i) => {
    given.append(h("h4", undefined, `Remembered text ${i + 1}${r.kind ? ` (${r.kind})` : ""}${r.text_file ? ` from ${r.text_file}` : ""}`), h("pre", undefined, r.text ?? ""));
  });
  out.push(given);
  const asserted = h("details");
  asserted.append(h("summary", undefined, "What the case expects (its definition now) and its settings"));
  asserted.append(h("pre", undefined, JSON.stringify({ expected: d.expected ?? null, params: d.params ?? null }, null, 2)));
  out.push(asserted);

  const trajs = e.trajectories ?? [];
  if (trajs.length) {
    out.push(h("h4", undefined, "ATIF traces"));
    const ul = h("ul", "traces");
    for (const t of trajs) {
      const li = h("li");
      const btn = h("button", "small", "View trace");
      btn.type = "button";
      btn.addEventListener("click", () => void showTrace(t.trajectory_id, `${e.eval}/${e.name} · #${t.seq} ${t.kind} (run #${run.id})`));
      li.append(btn, " ", h("strong", undefined, `#${t.seq} ${t.kind}`), h("span", "muted", ` ${t.steps} step${t.steps === 1 ? "" : "s"}`));
      ul.append(li);
    }
    out.push(ul);
  }
  return out;
}

// The right pane: the way to rerun the case with other models, and the commentaries on the case (newest first) with, for a case that
// failed, the way to ask for another.
function caseRight(d: CaseDebug, run: Run, experiment: string): HTMLElement[] {
  const out: HTMLElement[] = [...rerunSection(d, run, experiment), h("h3", undefined, "LLM commentary and recommendation")];
  const list = h("div");
  const render = (items: CaseCommentary[]): void => {
    if (items.length === 0) {
      list.replaceChildren(h("p", "muted", d.commentable ? "No commentary yet. Ask the LLM to diagnose this failure." : "This case did not fail, so there is nothing to comment on."));
      return;
    }
    list.replaceChildren(...items.map((c, i) => commentaryBlock(c, i > 0)));
  };
  render(d.commentaries);
  if (d.commentable) {
    const model = h("input");
    model.type = "text";
    model.value = d.default_model;
    model.title = "The model that writes the commentary";
    const msg = h("p", "muted");
    const btn = h("button", undefined, d.commentaries.length ? "Ask again" : "Ask LLM for commentary");
    btn.type = "button";
    btn.disabled = !d.commentary_enabled;
    if (!d.commentary_enabled) msg.textContent = "The server has no LLM (the Anthropic API key is missing), so a commentary cannot be made.";
    btn.addEventListener("click", () => {
      btn.disabled = true;
      msg.className = "muted";
      msg.textContent = "Asking the LLM… (this takes a while)";
      fetch(`api/evals/case_runs/${d.case_run_id}/commentary`, {
        method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ model: model.value.trim() }),
      }).then(async (res) => {
        if (!res.ok) {
          let text = (await res.text()).trim();
          try { text = (JSON.parse(text) as { error?: string }).error ?? text; } catch { /* not JSON: show it as it is */ }
          throw new Error(`${res.status}: ${text}`);
        }
        const c = (await res.json()) as CaseCommentary;
        d.commentaries = [c, ...d.commentaries];
        render(d.commentaries);
        btn.textContent = "Ask again";
        msg.textContent = "";
      }).catch((err: unknown) => {
        msg.className = "error";
        msg.textContent = `The commentary failed: ${errText(err)}`;
      }).finally(() => { btn.disabled = false; });
    });
    const ask = h("div", "commentary-ask");
    ask.append(model, btn);
    out.push(ask, msg);
  }
  out.push(list);
  return out;
}

//---------------------------------------------------------------------------------------------

// " (remember opus, query sonnet)" for the models of a rerun, "" if it kept them all.
function rerunModels(r: RerunInfo): string {
  const parts = [r.remember_model ? `remember ${r.remember_model}` : "", r.query_model ? `query ${r.query_model}` : ""].filter(Boolean);
  return parts.length ? ` (${parts.join(", ")})` : "";
}

// A select of the models of the price table; "" is the first option: the stages keep the models they had.
function modelSelect(models: string[], title: string): HTMLSelectElement {
  const sel = h("select");
  sel.title = title;
  sel.append(new Option("as it ran", ""));
  for (const m of models) sel.append(new Option(m, m));
  return sel;
}

// The rerun of the case with a model of the price table for the remember pipeline and/or for the query, to see whether a more
// powerful model gets it right. One model per pipeline is enough: the server uses it for every LLM stage of that pipeline. The rerun is a
// new run in the experiment run that owns this case run; its results open in the rerun modal. Nothing for a case that has not ended, or
// when the server cannot rerun (no LLM).
function rerunSection(d: CaseDebug, run: Run, experiment: string): HTMLElement[] {
  if (!d.rerun_options || !(["completed", "errored", "cancelled"] as Status[]).includes(d.status)) return [];
  const out: HTMLElement[] = [h("h3", undefined, "Re-run this case with another model")];
  out.push(h("p", "muted", "Runs the case again from the same input, as a new run of this experiment run. The model picked for a pipeline is used for all of its LLM stages; “as it ran” leaves that pipeline's models as they were. It costs LLM calls."));
  const remember = modelSelect(d.rerun_options.models, "The model of every LLM stage of the remember pipeline");
  const query = modelSelect(d.rerun_options.models, "The model of the query (its answering call and the detection of places)");
  const form = h("div", "rerun-form");
  const field = (label: string, sel: HTMLSelectElement): HTMLElement => {
    const l = h("label", undefined, label);
    l.append(sel);
    return l;
  };
  const btn = h("button", undefined, "Re-run case");
  btn.type = "button";
  const msg = h("p", "muted");
  form.append(field("Remember model", remember), field("Query model", query), btn);
  btn.addEventListener("click", () => {
    btn.disabled = true;
    msg.className = "muted";
    msg.textContent = "Starting the rerun…";
    fetch(`api/evals/case_runs/${d.case_run_id}/rerun`, {
      method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify({ remember_model: remember.value, query_model: query.value }),
    }).then(async (res) => {
      if (!res.ok) {
        let text = (await res.text()).trim();
        try { text = (JSON.parse(text) as { error?: string }).error ?? text; } catch { /* not JSON: show it as it is */ }
        throw new Error(`${res.status}: ${text}`);
      }
      const started = (await res.json()) as RerunStarted;
      msg.textContent = `Started: case run #${started.case_run_id}.`;
      void showRun(started.experiment_run_id ?? run.id, experiment); // the run behind lists the new case run
      void showRerun(started, d, run, experiment, { of: d.case_run_id, remember_model: remember.value, query_model: query.value });
    }).catch((err: unknown) => {
      msg.className = "error";
      msg.textContent = `The rerun could not be started: ${errText(err)}`;
    }).finally(() => { btn.disabled = false; });
  });
  out.push(form, msg);
  return out;
}

const RERUN_POLL_MS = 3000;
let rerunWatch = 0; // identifies the rerun the modal follows; a newer one, or closing the modal, stops the older poll

// Opens the rerun modal on a rerun that was just started and follows it until it ends: the status while it runs, then its result next
// to the case run it repeated (verdict, cost, models), its checks, the graph and temporal plot it built, and its traces. It is the
// same case run the run's list has, so it can also be opened from there (with its commentary).
async function showRerun(started: RerunStarted, orig: CaseDebug, run: Run, experiment: string, info: RerunInfo): Promise<void> {
  const modal = $<HTMLDialogElement>("rerun-modal");
  const status = $("rerun-status");
  const actions = $("rerun-actions");
  const content = $("rerun-content");
  const watch = ++rerunWatch;
  const title = `${orig.eval}/${orig.name} · rerun${rerunModels(info)} (case run #${started.case_run_id})`;
  $("rerun-title").textContent = title;
  actions.replaceChildren();
  content.replaceChildren(h("p", "muted", "Starting…"));
  status.className = "muted";
  status.textContent = "";
  if (!modal.open) modal.showModal();

  const tick = async (): Promise<void> => {
    if (watch !== rerunWatch || !modal.open) return;
    let d: CaseDebug;
    try {
      d = await getJSON<CaseDebug>(`api/evals/case_runs/${started.case_run_id}/debug`);
    } catch (err) {
      if (watch !== rerunWatch) return;
      status.className = "error";
      status.textContent = `Failed to read the rerun: ${errText(err)}`;
      return;
    }
    if (watch !== rerunWatch || !modal.open) return;
    status.className = "muted";
    status.replaceChildren(statusBadge(d.status), " ", verdictBadge(d.verdict));
    if (d.status === "running" || d.status === "pending") {
      const secs = Math.max(0, Math.round((Date.now() - new Date(d.started_at).getTime()) / 1000));
      content.replaceChildren(
        h("p", "muted", `Running for ${secs} s. The case remembers its texts and answers its question again with the chosen models; this takes about as long as the case did. The page can be closed: the result is recorded in the experiment run either way.`),
        rerunComparison(orig, d, started.case_run_id, info),
      );
      window.setTimeout(() => void tick(), RERUN_POLL_MS);
      return;
    }
    // Ended: the case run as the run lists it (with its traces), if the run can be read.
    let cr: CaseRun = { id: d.case_run_id, eval: d.eval, name: d.name, status: d.status, steps: 0, cost_usd: d.cost_usd, started_at: d.started_at, finished_at: d.finished_at ?? "", trajectories: [] };
    let xrun: Run = run;
    if (started.experiment_run_id !== undefined) {
      try {
        xrun = await getJSON<Run>(`api/evals/runs/${started.experiment_run_id}`);
        cr = (xrun.case_runs ?? []).find((c) => c.id === started.case_run_id) ?? cr;
      } catch { /* shown without its traces */ }
      void showRun(started.experiment_run_id, experiment); // the list behind now has its result
    }
    if (watch !== rerunWatch || !modal.open) return;
    const btns: HTMLElement[] = [];
    if (d.dataset_id != null) {
      const gbtn = h("button", "small", "Knowledge graph");
      gbtn.type = "button";
      gbtn.addEventListener("click", () => void showGraphAt(title, `api/evals/case_runs/${d.case_run_id}/graph`));
      const tbtn = h("button", "small", "Temporal plot");
      tbtn.type = "button";
      tbtn.addEventListener("click", () => openTemporalAt(`${title} · temporal changes`, `api/evals/case_runs/${d.case_run_id}/temporal`));
      btns.push(gbtn, tbtn);
    }
    if (started.experiment_run_id !== undefined) btns.push(caseBookmark(xrun, cr, experiment));
    actions.replaceChildren(...btns);
    content.replaceChildren(
      rerunComparison(orig, d, started.case_run_id, info),
      h("p", "muted", "It is in the experiment run's list like any case: open it there for its commentary."),
      ...caseLeft(d, xrun, cr),
    );
  };
  await tick();
}

// The case run that was rerun next to its rerun: the models, how each ended, what it cost.
function rerunComparison(orig: CaseDebug, re: CaseDebug, reID: number, info: RerunInfo): HTMLElement {
  const { table: tbl, body } = table([["", false], ["Case run", false], ["Remember model", false], ["Query model", false], ["Status", false], ["Verdict", false], ["Cost", true], ["Duration", true]]);
  const asRan = "as it ran";
  const row = (label: string, id: number, remember: string | undefined, query: string | undefined, d: CaseDebug): void => {
    const tr = body.insertRow();
    tr.insertCell().textContent = label;
    tr.insertCell().textContent = `#${id}`;
    tr.insertCell().textContent = remember || asRan;
    tr.insertCell().textContent = query || asRan;
    tr.insertCell().append(statusBadge(d.status));
    tr.insertCell().append(verdictBadge(d.verdict));
    numCell(tr, fmtCost(d.cost_usd));
    numCell(tr, fmtDuration(d.started_at, d.finished_at));
  };
  row("Original", orig.case_run_id, orig.rerun?.remember_model, orig.rerun?.query_model, orig);
  row("Rerun", reID, info.remember_model, info.query_model, re);
  return tbl;
}

const commentaryLabel = /^(Cause|Where|Evidence|Suggestion|Final advice):/;

// One commentary: who wrote it, when, what it cost, and its text with the labels (Cause, Where, ...) in bold.
function commentaryBlock(c: CaseCommentary, old: boolean): HTMLElement {
  const box = h("div");
  const cost = c.cost_usd > 0 ? `$${c.cost_usd.toFixed(4)}` : "cost unknown";
  box.append(h("p", "muted", `${c.model} · ${fmtTime(c.created_at)} UTC · ${c.input_tokens} in, ${c.output_tokens} out · ${cost}`));
  const body = h("div", old ? "commentary old" : "commentary");
  c.text.split("\n").forEach((line, i, all) => {
    const m = commentaryLabel.exec(line);
    if (m) body.append(h("span", "commentary-label", m[0]), line.slice(m[0].length));
    else body.append(line);
    if (i < all.length - 1) body.append("\n");
  });
  box.append(body);
  return box;
}

// The artifacts of one trajectory: for a remember call the input artifact the caller gave, for a query the artifacts
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
  registerBookmarkOpener("eval_case", openBookmarkedCase);
  const modal = $<HTMLDialogElement>("run-modal");
  $("run-close").addEventListener("click", () => modal.close());
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
  const caseModal = $<HTMLDialogElement>("case-modal");
  $("case-close").addEventListener("click", () => caseModal.close());
  caseModal.addEventListener("click", (e) => { if (e.target === caseModal) caseModal.close(); });
  const rerunModal = $<HTMLDialogElement>("rerun-modal");
  rerunModal.addEventListener("close", () => { rerunWatch++; }); // stops following the rerun (it goes on on the server)
  $("rerun-close").addEventListener("click", () => rerunModal.close());
  rerunModal.addEventListener("click", (e) => { if (e.target === rerunModal) rerunModal.close(); });
  $("evals-refresh").addEventListener("click", () => {
    void loadOverview();
    if (selectedExperiment !== undefined) void loadExperiment(selectedExperiment);
  });
  void loadOverview();
}
