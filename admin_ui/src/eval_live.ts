// Live view of a trained-model run: the dialog #eval-modal follows the SSE feed of a training data AUDIT (../training/audits/<id>/events,
// the model asked its own training dataset) or of an EVAL (/v1/gb/training/evals/<id>/events), which have the same shape, and shows each
// question as the model answers it (the reference answer next to the model's), then its score and the judge's reason when the batch
// it is in is judged (the judge grades a batch of questions per call). One sees how the model behaves from the first question on.
// Closing the dialog leaves the run going.

// The statuses and verdicts of the runs and cases of a trained model are those of the model of internal/evals/core (and of
// internal/training_audits/core, which has the same statuses), shown as they are: a run or a case is pending, running, completed,
// errored or cancelled (did it run to the end), and a completed eval case has the verdict pass or fail (the judge's score is at least
// core.TrainingPassScore or below). In the live feed a case the model answered and the judge has not scored yet is running. An audit
// has no verdicts, only scores.
export type Status = "pending" | "running" | "completed" | "errored" | "cancelled";
export type Verdict = "pass" | "fail";
// core.Counts: every case is in exactly one of pending, running, completed, errored, cancelled; passed and failed split the completed
// ones that have a verdict (an audit's counts have neither).
export interface Counts { total: number; pending: number; running: number; completed: number; errored: number; cancelled: number; passed?: number; failed?: number }

// A run of a trained model, of either kind (training.RunView). An eval run has `eval` (the eval whose cases were asked); an
// audit run has `dataset` and `dataset_sha256` (the training dataset whose examples were asked).
export interface TrainedModelRun {
  id: number; trained_model_id: number; kind: "eval" | "audit"; status: Status; eval?: string; dataset?: string; dataset_sha256?: string;
  params: { batch_size?: number; max_tokens?: number; temperature?: number; limit?: number };
  judge_model: string; counts: Counts; num_answered: number; num_judged: number; num_correct: number;
  num_partial: number; num_incorrect: number; score_sum: number; num_batches: number; judge_calls: number; judge_input_tokens: number;
  judge_output_tokens: number; judge_cost_usd: number; error: string; created_at: string; finished_at: string | null;
}
// One case of a run (training.CaseView); an eval's cases have a `name`, an audit's (dataset examples) have none.
export interface TrainedModelCase {
  idx: number; name?: string; batch: number; instruction: string; input: string; reference: string; response: string;
  status: Status; verdict?: Verdict;
  error: string; score: number | null; reason: string; input_tokens: number; output_tokens: number; duration_ms: number;
}
// The two kinds of run of a trained model: a training data audit (URL segment "audits") and an eval ("evals"). The runs of both
// have the shape of TrainedModelRun, and their cases that of TrainedModelCase, and the feed's events are the same for both.
export type RunKind = "audits" | "evals";
interface FeedEvent { type: "snapshot" | "answer" | "batch" | "end"; run?: TrainedModelRun; cases?: TrainedModelCase[] }

// The audits are served by internal/training_audits, whose JSON differs a little from that of the evals': the run has its cost in
// `cost_usd` and no `kind`, and its case runs are `case_runs` with `judge_score`/`judge_reason`. These two functions turn them into
// the shapes above (the statuses are already the same), so the rest of the UI treats both kinds alike. The audit run is core.AuditRun, its case run core.AuditCaseRun, its event
// training_audits.Event (all in internal/training_audits).
interface AuditRunJSON extends Omit<TrainedModelRun, "kind" | "judge_cost_usd" | "dataset_sha256"> {
  dataset_sha256?: string; cost_usd: number; case_runs?: AuditCaseRunJSON[];
}
interface AuditCaseRunJSON extends Omit<TrainedModelCase, "score" | "reason" | "name" | "verdict"> {
  judge_score?: number | null; judge_reason?: string;
}
export function auditRun(r: AuditRunJSON): TrainedModelRun {
  const { cost_usd, case_runs: _cases, ...rest } = r;
  return { ...rest, kind: "audit", params: r.params ?? {}, judge_cost_usd: cost_usd };
}
export function auditCase(c: AuditCaseRunJSON): TrainedModelCase {
  const { judge_score, judge_reason, ...rest } = c;
  return { ...rest, score: judge_score ?? null, reason: judge_reason ?? "" };
}
// The run and the cases of the feed event (data is the parsed JSON of the event) of a run of that kind.
function feedEvent(kind: RunKind, data: Record<string, unknown>): FeedEvent {
  if (kind !== "audits") return data as unknown as FeedEvent;
  const d = data as { type: FeedEvent["type"]; run?: AuditRunJSON; case_runs?: AuditCaseRunJSON[] };
  return { type: d.type, run: d.run && auditRun(d.run), cases: d.case_runs?.map(auditCase) };
}

const MAX_SCORE = 5; // the judge's scale: 0 to 5

const el = <T extends HTMLElement>(id: string): T => {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
};
function node<K extends keyof HTMLElementTagNameMap>(tag: K, cls = "", text = ""): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text) e.textContent = text; // textContent only: never inject server data as HTML
  return e;
}

// How a case looks: its status while it is not completed (pending, running, errored, cancelled), and, once it is, the band of the
// judge's score: good (4-5, the correct ones), mid (2-3, partial) or bad (0-1, incorrect); in an audit a 0 is "zero" (a total failure).
const lookOf = (i: TrainedModelCase): string =>
  i.status !== "completed" ? i.status : i.score === null ? "done" : i.score >= 4 ? "good" : i.score >= 2 ? "mid"
    : i.score === 0 && watchingKind === "audits" ? "zero" : "bad";

// What the open dialog follows.
let stream: EventSource | undefined;
let watching = -1;
let watchingKind: RunKind = "audits";
let run: TrainedModelRun | undefined;
let ended = false;
let cases = new Map<number, TrainedModelCase>();
let cards = new Map<number, HTMLElement>();
let squares: HTMLElement[] = [];
let onChange: (() => void) | undefined;
let ticker: number | undefined;
let dirty = false; // a summary redraw is queued

function stop(): void {
  stream?.close();
  stream = undefined;
  if (ticker !== undefined) { clearInterval(ticker); ticker = undefined; }
}

function reset(): void {
  run = undefined;
  ended = false;
  cases = new Map();
  cards = new Map();
  squares = [];
  el("eval-live-feed").replaceChildren();
  el("eval-live-strip").replaceChildren();
  el("eval-live-summary").textContent = "";
  el("eval-live-error").textContent = "";
  el("eval-live-conn").textContent = "";
  el<HTMLProgressElement>("eval-live-progress").value = 0;
}

// The badge of a status as it is: completed is ok, errored an error, cancelled is muted, running and pending are active.
export function statusBadge(status: string): HTMLElement {
  const cls = status === "completed" ? "badge-ok" : status === "errored" ? "badge-error" : status === "cancelled" ? "badge-unknown" : "badge-active";
  return node("span", `badge ${cls}`, status);
}

// The badge of a verdict (pass or fail).
export function verdictBadge(verdict: Verdict): HTMLElement {
  return node("span", `badge ${verdict === "pass" ? "badge-ok" : "badge-error"}`, verdict);
}

const fmtSecs = (ms: number): string => `${(ms / 1000).toFixed(1)}s`;

function textBlock(label: string, text: string, cls = ""): HTMLElement {
  const box = node("div", `ev-block ${cls}`);
  box.append(node("div", "ev-label", label), node("pre", "ev-text", text));
  return box;
}

function buildCard(i: TrainedModelCase): HTMLElement {
  const v = lookOf(i);
  const card = node("article", `ev-card ev-${v}`);
  card.dataset.idx = String(i.idx);
  const head = node("div", "ev-head");
  head.append(node("span", "ev-num", `#${i.idx + 1}`));
  if (i.name) head.append(node("span", "ev-name", i.name));
  if (i.status === "completed") {
    if (i.score !== null) head.append(node("span", `ev-score score-${v}`, `${i.score} / ${MAX_SCORE}`));
    if (i.verdict) head.append(verdictBadge(i.verdict));
  } else {
    // running is a case the model answered whose batch the judge has not scored yet; once the run is over it never will be
    const badge = statusBadge(i.status);
    if (i.status === "running") badge.textContent = ended ? "running · never judged" : "running · awaiting judge";
    head.append(badge);
  }
  const stats = i.status === "errored" && !i.response ? "" : `${fmtSecs(i.duration_ms)} · ${i.input_tokens} in / ${i.output_tokens} out tokens`;
  if (stats) head.append(node("span", "muted", stats));
  card.append(head);
  card.append(textBlock("Question", i.input ? `${i.instruction}\n\n${i.input}` : i.instruction, "ev-question"));
  const cols = node("div", "ev-cols");
  cols.append(textBlock(watchingKind === "evals" ? "Expected answer" : "Dataset answer", i.reference), textBlock("Model answer", i.status === "errored" && !i.response ? "(no answer)" : i.response === "" ? "(empty)" : i.response, "ev-model"));
  card.append(cols);
  if (i.status === "errored") card.append(node("div", "error ev-reason", i.error));
  else if (i.reason) card.append(node("div", "muted ev-reason", `Judge: ${i.reason}`));
  return card;
}

// Adds or updates cases. A judged or errored case is never turned back into a running one (a reconnect's snapshot can race the feed).
function apply(list: TrainedModelCase[]): void {
  const feed = el("eval-live-feed");
  const follow = el<HTMLInputElement>("eval-live-follow").checked;
  for (const i of list) {
    const old = cases.get(i.idx);
    if (old && old.status !== "running" && i.status === "running") continue;
    cases.set(i.idx, i);
    const card = buildCard(i);
    const prev = cards.get(i.idx);
    if (prev) prev.replaceWith(card);
    else {
      // in dataset order (they arrive in order, bar a reconnect's snapshot)
      const next = [...cards.entries()].filter(([k]) => k > i.idx).sort((a, b) => a[0] - b[0])[0];
      if (next) next[1].before(card); else feed.append(card);
    }
    cards.set(i.idx, card);
    const sq = squares[i.idx];
    if (sq) sq.className = `ev-sq ev-sq-${lookOf(i)}`;
  }
  if (follow) feed.parentElement!.scrollTop = feed.parentElement!.scrollHeight;
  queueSummary();
}

function queueSummary(): void {
  if (dirty) return;
  dirty = true;
  requestAnimationFrame(() => { dirty = false; drawSummary(); });
}

function drawSummary(): void {
  const total = run?.counts.total ?? cases.size;
  let judged = 0, errored = 0, good = 0, mid = 0, bad = 0, scoreSum = 0, ms = 0, answered = 0;
  for (const i of cases.values()) {
    if (i.status === "errored") errored++;
    else { answered++; ms += i.duration_ms; }
    if (i.status !== "completed" || i.score === null) continue;
    judged++;
    scoreSum += i.score;
    if (i.score >= 4) good++; else if (i.score >= 2) mid++; else bad++;
  }
  el<HTMLProgressElement>("eval-live-progress").max = Math.max(total, 1);
  el<HTMLProgressElement>("eval-live-progress").value = cases.size;
  const parts = [`${cases.size} / ${total} answered`, `${judged} judged${errored ? ` · ${errored} errored` : ""}`];
  if (judged > 0) {
    parts.push(`mean score ${(scoreSum / judged).toFixed(2)} / ${MAX_SCORE}`, `${Math.round((100 * good) / judged)}% correct`,
      `${good} correct · ${mid} partial · ${bad} incorrect`);
  }
  if (answered > 0) parts.push(`avg answer ${fmtSecs(ms / answered)}`);
  el("eval-live-summary").textContent = parts.join(" · ");
  const mean = judged > 0 ? scoreSum / judged : null;
  el("eval-live-summary").className = `eval-summary ${mean === null ? "" : mean >= 4 ? "score-good" : mean >= 2 ? "score-mid" : "score-bad"}`;
  if (run) {
    const badge = el("eval-live-state");
    badge.replaceChildren(statusBadge(run.status));
    el<HTMLButtonElement>("eval-live-cancel").hidden = run.status !== "running";
    el("eval-live-error").textContent = run.error ? `Error: ${run.error}` : "";
    const started = new Date(run.created_at).getTime();
    const until = run.finished_at ? new Date(run.finished_at).getTime() : Date.now();
    const secs = Math.max(0, Math.floor((until - started) / 1000));
    el("eval-live-elapsed").textContent = Number.isNaN(started) ? "" : secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${String(secs % 60).padStart(2, "0")}s`;
  }
}

function setRun(r: TrainedModelRun): void {
  const first = run === undefined || run.counts.total !== r.counts.total;
  run = r;
  if (first) { // one square per question, to see the whole run at a glance
    const strip = el("eval-live-strip");
    strip.replaceChildren();
    squares = [];
    for (let k = 0; k < r.counts.total; k++) {
      const sq = node("button", "ev-sq ev-sq-todo");
      sq.type = "button";
      sq.title = `Question ${k + 1}`;
      sq.addEventListener("click", () => {
        el<HTMLInputElement>("eval-live-follow").checked = false;
        cards.get(k)?.scrollIntoView({ block: "center", behavior: "smooth" });
      });
      squares.push(sq);
      strip.append(sq);
    }
    for (const i of cases.values()) squares[i.idx]?.classList.replace("ev-sq-todo", `ev-sq-${lookOf(i)}`);
  }
  queueSummary();
}

// Opens the dialog on the feed of run `id` of that kind and follows it until it ends. cb is called when the run ended.
export function watchEval(kind: RunKind, id: number, title: string, cb?: () => void): void {
  const modal = el<HTMLDialogElement>("eval-modal");
  stop();
  reset();
  watching = id;
  watchingKind = kind;
  onChange = cb;
  el("eval-live-cancel").textContent = "Stop";
  el("eval-live-title").textContent = title;
  el<HTMLInputElement>("eval-live-follow").checked = true;
  el("eval-live-cancel").hidden = true;
  if (!modal.open) modal.showModal();

  const es = new EventSource(`../training/${kind}/${id}/events`);
  stream = es;
  const conn = el("eval-live-conn");
  conn.textContent = "Connecting…";
  es.onopen = () => { conn.textContent = "Live."; };
  es.onerror = () => { conn.textContent = "Connection lost, reconnecting…"; }; // EventSource retries; a new snapshot catches up
  const on = (type: FeedEvent["type"], fn: (e: FeedEvent) => void): void =>
    es.addEventListener(type, (m) => fn(feedEvent(kind, JSON.parse((m as MessageEvent<string>).data) as Record<string, unknown>)));
  on("snapshot", (e) => {
    if (e.run) setRun(e.run);
    apply(e.cases ?? []);
  });
  on("answer", (e) => apply(e.cases ?? []));
  on("batch", (e) => {
    if (e.run) setRun(e.run);
    apply(e.cases ?? []);
  });
  on("end", (e) => {
    ended = true;
    if (e.run && e.run.status) setRun(e.run);
    // answers the judge never scored (a cancelled or errored eval): redraw them as "not judged"
    for (const i of cases.values()) {
      if (i.status !== "running") continue;
      const card = buildCard(i);
      cards.get(i.idx)?.replaceWith(card);
      cards.set(i.idx, card);
    }
    conn.textContent = e.run?.status === "completed" ? "Finished." : `Ended (${e.run?.status ?? "unknown"}).`;
    stop();
    queueSummary();
    onChange?.();
  });
  ticker = window.setInterval(queueSummary, 1000); // the elapsed time of a running eval
}

export function initEvalLive(cancel: (kind: RunKind, runID: number) => Promise<void>): void {
  const modal = el<HTMLDialogElement>("eval-modal");
  modal.addEventListener("close", () => { stop(); watching = -1; });
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
  el("eval-live-close").addEventListener("click", () => modal.close());
  el("eval-live-cancel").addEventListener("click", () => {
    const btn = el<HTMLButtonElement>("eval-live-cancel");
    btn.disabled = true;
    void cancel(watchingKind, watching).finally(() => { btn.disabled = false; });
  });
  el<HTMLInputElement>("eval-live-problems").addEventListener("change", (e) =>
    el("eval-live-feed").classList.toggle("only-problems", (e.target as HTMLInputElement).checked));
  // Scrolling up stops following; the checkbox turns it on again.
  const body = el("eval-live-feed").parentElement as HTMLElement;
  body.addEventListener("scroll", () => {
    if (body.scrollHeight - body.scrollTop - body.clientHeight > 60) el<HTMLInputElement>("eval-live-follow").checked = false;
  });
  el<HTMLInputElement>("eval-live-follow").addEventListener("change", (e) => {
    if ((e.target as HTMLInputElement).checked) body.scrollTop = body.scrollHeight;
  });
}
