// Evals block of the admin UI: experiments (counts + listing), their runs, a run's results with their records, and
// the ATIF trajectories of each case (opened as a flow graph by trace.ts). Talks to /admin/api/evals/*.

import { artifactsPanel, caseBookmark, rerunModels, setRunRefresher, showCase } from "./case_modal.js";
import {
  $, countsLine, errText, fmtCost, fmtDuration, fmtTime, getJSON, h, numCell, props, stat, statusBadge, statusLine, table, verdictBadge,
  type CaseRun, type Counts, type EvalWebSearches, type Run, type Status,
} from "./eval_common.js";
import { showGraphAt } from "./graph.js";
import { showCaseHistory } from "./history.js";
import { openTemporalCase } from "./temporal.js";
import { showTrace } from "./trace.js";
import { showWebSearches, totalsLine } from "./websearch.js";

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
      openTemporalCase(`${e.eval}/${e.name} (run #${run.id}) · temporal changes`, e.id);
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


export function initEvals(): void {
  const modal = $<HTMLDialogElement>("run-modal");
  $("run-close").addEventListener("click", () => modal.close());
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
  // A rerun made in the case modal changes its run: draw it again if it is open (it is not opened if the case modal came from elsewhere).
  setRunRefresher((runID, experiment) => { if (modal.open) void showRun(runID, experiment); });
  $("evals-refresh").addEventListener("click", () => {
    void loadOverview();
    if (selectedExperiment !== undefined) void loadExperiment(selectedExperiment);
  });
  void loadOverview();
}
