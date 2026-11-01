// The case modal of the admin UI: one eval case run (checks, graph and temporal plot, traces, rerun with other models, LLM commentary), and the
// rerun modal that follows a rerun. It does not belong to any view: any view can open it with showCase (the evals view from a run, the
// bookmarks view from a bookmark) and closing it leaves the operator where they were. The modals are in index.html.

import { bookmarkButton, registerBookmarkOpener, type Bookmark } from "./bookmarks.js";
import {
  $, errText, fmtCost, fmtDuration, fmtTime, getJSON, h, numCell, statusBadge, table, verdictBadge,
  type CaseCommentary, type CaseDebug, type CaseRun, type RerunInfo, type RerunStarted, type Run, type Status, type Trajectory,
} from "./eval_common.js";
import { showGraphAt } from "./graph.js";
import { openTemporalCase } from "./temporal.js";
import { showTrace } from "./trace.js";

// Called when a rerun changes a run (it was started, it ended), so a view that shows that run can draw it again. None if no view does.
let refreshRun: ((runID: number, experiment: string) => void) | undefined;
export function setRunRefresher(fn: (runID: number, experiment: string) => void): void {
  refreshRun = fn;
}

// right the LLM's commentary and recommendation, which can be asked for when the case failed. The header has the buttons of the
// knowledge graph and the temporal plot the case built, so the failure can be debugged against everything it ran on.
export async function showCase(run: Run, e: CaseRun, experiment: string): Promise<void> {
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
      tbtn.addEventListener("click", () => openTemporalCase(`${title} · temporal changes`, e.id));
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
export function caseBookmark(run: Run, e: CaseRun, experiment: string): HTMLButtonElement {
  return bookmarkButton({
    id: `eval_case:${e.id}`,
    kind: "eval_case",
    title: `${e.eval}/${e.name}`,
    detail: `${experiment} · run #${run.id}`,
    ref: { case_run_id: e.id, run_id: run.id, experiment },
  });
}

// Opens a bookmarked case: only the case modal, on top of whatever view the operator is in (the run it belongs to is not opened).
async function openBookmarkedCase(b: Bookmark): Promise<void> {
  const run = await getJSON<Run>(`api/evals/runs/${Number(b.ref.run_id)}`);
  const e = (run.case_runs ?? []).find((c) => c.id === Number(b.ref.case_run_id));
  if (!e) throw new Error("the case run is not in its run any more");
  await showCase(run, e, String(b.ref.experiment));
}

// The left pane: the failure, the checks that failed and the ones that held, the case as it ran and as it is defined, the traces.
function caseLeft(d: CaseDebug, run: Run, e: CaseRun): HTMLElement[] {
  const out: HTMLElement[] = [h("h3", undefined, `Checks (${d.problems.length} failed, ${d.passed_checks.length} held)`)];
  if (d.error) out.push(h("p", "error", `The case could not run to the end: ${d.error}`));
  // What went differently from the settings without failing the case (e.g. a model that does not take the thinking level asked for).
  if (d.warnings?.length) {
    const ul = h("ul", "case-warnings");
    for (const w of d.warnings) ul.append(h("li", undefined, w));
    out.push(h("h4", "case-warning-title", "Warnings"), ul);
  }
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
  const out: HTMLElement[] = [...rerunSection(d, run, experiment), ...rerunList(d, run, experiment), h("h3", undefined, "LLM commentary and recommendation")];
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

// "opus, thinking high" for the model and the thinking level a rerun gave a pipeline; "" if it kept both.
function pipelineSetting(model?: string, reasoning?: string): string {
  return [model ?? "", reasoning ? `thinking ${reasoning}` : ""].filter(Boolean).join(", ");
}

// " (remember opus, thinking high; query sonnet)" for what a rerun changed, "" if it kept everything.
export function rerunModels(r: RerunInfo): string {
  const parts = [["remember", pipelineSetting(r.remember_model, r.remember_reasoning)], ["query", pipelineSetting(r.query_model, r.query_reasoning)]]
    .filter(([, s]) => s).map(([what, s]) => `${what} ${s}`);
  return parts.length ? ` (${parts.join("; ")})` : "";
}

// A select of the given values (the models of the price table, or the thinking levels); "" is the first option: the stages keep what they had.
function optionSelect(values: string[], title: string): HTMLSelectElement {
  const sel = h("select");
  sel.title = title;
  sel.append(new Option("as it ran", ""));
  for (const v of values) sel.append(new Option(v, v));
  return sel;
}

// The rerun of the case with a model of the price table and/or a thinking level for the remember pipeline and/or for the query, to see
// whether a more powerful model (or more thinking) gets it right. One model and one level per pipeline is enough: the server uses them
// for every LLM stage of that pipeline. The rerun is a
// new run in the experiment run that owns this case run; its results open in the rerun modal. Nothing for a case that has not ended, or
// when the server cannot rerun (no LLM).
function rerunSection(d: CaseDebug, run: Run, experiment: string): HTMLElement[] {
  if (!d.rerun_options || !(["completed", "errored", "cancelled"] as Status[]).includes(d.status)) return [];
  const out: HTMLElement[] = [h("h3", undefined, "Re-run this case with another model or thinking level")];
  out.push(h("p", "muted", "Runs the case again from the same input, as a new run of this experiment run. The model and the thinking level picked for a pipeline are used for all of its LLM stages; “as it ran” leaves that setting as it was. It costs LLM calls."));
  const remember = optionSelect(d.rerun_options.models, "The model of every LLM stage of the remember pipeline");
  const query = optionSelect(d.rerun_options.models, "The model of the query (its answering call and the detection of places)");
  const levels = d.rerun_options.reasoning ?? [];
  const rememberThinking = optionSelect(levels, "The thinking level of every LLM stage of the remember pipeline");
  const queryThinking = optionSelect(levels, "The thinking level of the query (its answering call and the detection of places)");
  const form = h("div", "rerun-form");
  const field = (label: string, sel: HTMLSelectElement): HTMLElement => {
    const l = h("label", undefined, label);
    l.append(sel);
    return l;
  };
  const btn = h("button", undefined, "Re-run case");
  btn.type = "button";
  const msg = h("p", "muted");
  // Per pipeline, the model and the thinking level go one under the other in a tight column.
  const group = (...fields: HTMLElement[]): HTMLElement => {
    const g = h("div", "rerun-group");
    g.append(...fields);
    return g;
  };
  form.append(group(field("Remember model", remember), field("Remember thinking", rememberThinking)), group(field("Query model", query), field("Query thinking", queryThinking)), btn);
  btn.addEventListener("click", () => {
    btn.disabled = true;
    msg.className = "muted";
    msg.textContent = "Starting the rerun…";
    fetch(`api/evals/case_runs/${d.case_run_id}/rerun`, {
      method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify({
        remember_model: remember.value, query_model: query.value, remember_reasoning: rememberThinking.value, query_reasoning: queryThinking.value,
      }),
    }).then(async (res) => {
      if (!res.ok) {
        let text = (await res.text()).trim();
        try { text = (JSON.parse(text) as { error?: string }).error ?? text; } catch { /* not JSON: show it as it is */ }
        throw new Error(`${res.status}: ${text}`);
      }
      const started = (await res.json()) as RerunStarted;
      msg.textContent = `Started: case run #${started.case_run_id}.`;
      refreshRun?.(started.experiment_run_id ?? run.id, experiment); // the run list behind (if it is open) lists the new case run
      void showRerun(started, d, run, experiment, {
        of: d.case_run_id, remember_model: remember.value, query_model: query.value,
        remember_reasoning: rememberThinking.value, query_reasoning: queryThinking.value,
      });
    }).catch((err: unknown) => {
      msg.className = "error";
      msg.textContent = `The rerun could not be started: ${errText(err)}`;
    }).finally(() => { btn.disabled = false; });
  });
  out.push(form, msg);
  return out;
}

// The reruns this case run already has (and those of its reruns), one row each: the models and thinking levels of the remember pipeline
// and of the query ("as it ran": the pipeline kept its settings) and the final pass or fail (the status while it has no verdict: running, errored...).
// Nothing when there are none. A rerun that is in the run's list opens as a case of its own.
function rerunList(d: CaseDebug, run: Run, experiment: string): HTMLElement[] {
  const reruns = d.reruns ?? [];
  if (reruns.length === 0) return [];
  const { table: tbl, body } = table([["Case run", false], ["Remember", false], ["Query", false], ["Result", false], ["", false]]);
  for (const r of reruns) {
    const tr = body.insertRow();
    tr.insertCell().textContent = `#${r.case_run_id}`;
    tr.insertCell().textContent = pipelineSetting(r.remember_model, r.remember_reasoning) || "as it ran";
    tr.insertCell().textContent = pipelineSetting(r.query_model, r.query_reasoning) || "as it ran";
    tr.insertCell().append(r.verdict ? verdictBadge(r.verdict) : statusBadge(r.status));
    const cell = tr.insertCell();
    const e = (run.case_runs ?? []).find((c) => c.id === r.case_run_id);
    if (e) {
      const btn = h("button", "small", "Open");
      btn.type = "button";
      btn.addEventListener("click", () => void showCase(run, e, experiment));
      cell.append(btn);
    }
  }
  return [h("h3", undefined, `Re-runs (${reruns.length})`), tbl];
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
      refreshRun?.(started.experiment_run_id, experiment); // the list behind (if it is open) now has its result
    }
    if (watch !== rerunWatch || !modal.open) return;
    const btns: HTMLElement[] = [];
    if (d.dataset_id != null) {
      const gbtn = h("button", "small", "Knowledge graph");
      gbtn.type = "button";
      gbtn.addEventListener("click", () => void showGraphAt(title, `api/evals/case_runs/${d.case_run_id}/graph`));
      const tbtn = h("button", "small", "Temporal plot");
      tbtn.type = "button";
      tbtn.addEventListener("click", () => openTemporalCase(`${title} · temporal changes`, d.case_run_id));
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
  const { table: tbl, body } = table([["", false], ["Case run", false], ["Remember", false], ["Query", false], ["Status", false], ["Verdict", false], ["Cost", true], ["Duration", true]]);
  const asRan = "as it ran";
  const row = (label: string, id: number, r: RerunInfo | undefined, d: CaseDebug): void => {
    const tr = body.insertRow();
    tr.insertCell().textContent = label;
    tr.insertCell().textContent = `#${id}`;
    tr.insertCell().textContent = pipelineSetting(r?.remember_model, r?.remember_reasoning) || asRan;
    tr.insertCell().textContent = pipelineSetting(r?.query_model, r?.query_reasoning) || asRan;
    tr.insertCell().append(statusBadge(d.status));
    tr.insertCell().append(verdictBadge(d.verdict));
    numCell(tr, fmtCost(d.cost_usd));
    numCell(tr, fmtDuration(d.started_at, d.finished_at));
  };
  row("Original", orig.case_run_id, orig.rerun, orig);
  row("Rerun", reID, info, re);
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
export function artifactsPanel(t: Trajectory): HTMLElement {
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


export function initCaseModal(): void {
  registerBookmarkOpener("eval_case", openBookmarkedCase);
  const caseModal = $<HTMLDialogElement>("case-modal");
  $("case-close").addEventListener("click", () => caseModal.close());
  caseModal.addEventListener("click", (e) => { if (e.target === caseModal) caseModal.close(); });
  const rerunModal = $<HTMLDialogElement>("rerun-modal");
  rerunModal.addEventListener("close", () => { rerunWatch++; }); // stops following the rerun (it goes on on the server)
  $("rerun-close").addEventListener("click", () => rerunModal.close());
  rerunModal.addEventListener("click", (e) => { if (e.target === rerunModal) rerunModal.close(); });
}
