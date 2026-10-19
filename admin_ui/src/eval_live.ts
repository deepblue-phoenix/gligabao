// Live view of a trained-model run: the dialog #eval-modal follows the SSE feed of a training data AUDIT (../training/audits/<id>/events,
// the model asked its own training dataset) or of an EVAL (../training/evals/<id>/events), which have the same shape, and shows each
// question as the model answers it (the reference answer next to the model's), then its score and the judge's reason when the batch
// it is in is judged (the judge grades a batch of questions per call). One sees how the model behaves from the first question on.
// Closing the dialog leaves the run going.

// The statuses of the runs and cases of a trained model, the same for evals and audits. They are the words of
// internal/db/relational/model/trained_model_status.go (TrainedModelRun*, TrainedModelCase*): change them there and here.
export const RunStatus = { running: "running", completed: "completed", failed: "failed", cancelled: "cancelled" } as const;
export type RunStatus = (typeof RunStatus)[keyof typeof RunStatus];
export const CaseStatus = { judged: "judged", failed: "failed", answered: "answered" } as const; // answered: only in the live feed
export type CaseStatus = (typeof CaseStatus)[keyof typeof CaseStatus];

export interface EvalRun {
  id: number; trained_model_id: number; status: RunStatus; dataset: string; dataset_sha256: string;
  params: { batch_size?: number; max_tokens?: number; temperature?: number; limit?: number };
  judge_model: string; num_examples: number; num_answered: number; num_judged: number; num_failed: number; num_correct: number;
  num_partial: number; num_incorrect: number; score_sum: number; num_batches: number; judge_calls: number; judge_input_tokens: number;
  judge_output_tokens: number; judge_cost_usd: number; error: string; created_at: string; finished_at: string | null;
}
export interface EvalCase {
  idx: number; batch: number; instruction: string; input: string; reference: string; response: string;
  // CaseStatus.answered: the model answered and the judge has not scored it yet (only in the live feed)
  status: CaseStatus;
  error: string; score: number | null; reason: string; input_tokens: number; output_tokens: number; duration_ms: number;
}
// The two kinds of run of a trained model: a training data audit (URL segment "audits") and an eval ("evals"). The runs of both
// have the shape of EvalRun, and their cases that of EvalCase.
export type RunKind = "audits" | "evals";
// A feed event carries the run under the key of its kind: "audit" or "eval".
interface FeedEvent { type: "snapshot" | "answer" | "batch" | "end"; audit?: EvalRun; eval?: EvalRun; cases?: EvalCase[] }

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

// How an case is doing: pending (answered, awaiting the judge), good (4-5), mid (2-3), bad (0-1) or failed.
type Verdict = "pending" | "good" | "mid" | "bad" | "failed";
const verdictOf = (i: EvalCase): Verdict =>
  i.status === CaseStatus.failed ? "failed" : i.status === CaseStatus.answered || i.score === null ? "pending" : i.score >= 4 ? "good" : i.score >= 2 ? "mid" : "bad";

// What the open dialog follows.
let stream: EventSource | undefined;
let watching = -1;
let watchingKind: RunKind = "audits";
let run: EvalRun | undefined;
let ended = false;
let cases = new Map<number, EvalCase>();
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

function statusBadge(status: string): HTMLElement {
  const cls = status === RunStatus.completed ? "badge-ok" : status === RunStatus.failed ? "badge-error" : status === RunStatus.cancelled ? "badge-unknown" : "badge-active";
  return node("span", `badge ${cls}`, status);
}

const fmtSecs = (ms: number): string => `${(ms / 1000).toFixed(1)}s`;

function textBlock(label: string, text: string, cls = ""): HTMLElement {
  const box = node("div", `ev-block ${cls}`);
  box.append(node("div", "ev-label", label), node("pre", "ev-text", text));
  return box;
}

function buildCard(i: EvalCase): HTMLElement {
  const v = verdictOf(i);
  const card = node("article", `ev-card ev-${v}`);
  card.dataset.idx = String(i.idx);
  const head = node("div", "ev-head");
  head.append(node("span", "ev-num", `#${i.idx + 1}`));
  if (v === "pending") head.append(node("span", ended ? "badge badge-unknown" : "badge badge-active", ended ? "not judged" : "answered · awaiting judge"));
  else if (v === "failed") head.append(node("span", "badge badge-error", "failed"));
  else head.append(node("span", `ev-score score-${v}`, `${i.score} / ${MAX_SCORE}`));
  const stats = i.status === CaseStatus.failed && !i.response ? "" : `${fmtSecs(i.duration_ms)} · ${i.input_tokens} in / ${i.output_tokens} out tokens`;
  if (stats) head.append(node("span", "muted", stats));
  card.append(head);
  card.append(textBlock("Question", i.input ? `${i.instruction}\n\n${i.input}` : i.instruction, "ev-question"));
  const cols = node("div", "ev-cols");
  cols.append(textBlock("Dataset answer", i.reference), textBlock("Model answer", i.status === CaseStatus.failed && !i.response ? "(no answer)" : i.response === "" ? "(empty)" : i.response, "ev-model"));
  card.append(cols);
  if (i.status === CaseStatus.failed) card.append(node("div", "error ev-reason", i.error));
  else if (i.reason) card.append(node("div", "muted ev-reason", `Judge: ${i.reason}`));
  return card;
}

// Adds or updates cases. A judged or failed case is never turned back into an answered one (a reconnect's snapshot can race the feed).
function apply(list: EvalCase[]): void {
  const feed = el("eval-live-feed");
  const follow = el<HTMLInputElement>("eval-live-follow").checked;
  for (const i of list) {
    const old = cases.get(i.idx);
    if (old && old.status !== CaseStatus.answered && i.status === CaseStatus.answered) continue;
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
    if (sq) sq.className = `ev-sq ev-sq-${verdictOf(i)}`;
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
  const total = run?.num_examples ?? cases.size;
  let judged = 0, failed = 0, good = 0, mid = 0, bad = 0, scoreSum = 0, ms = 0, answered = 0;
  for (const i of cases.values()) {
    if (i.status === CaseStatus.failed) failed++;
    else { answered++; ms += i.duration_ms; }
    if (i.status !== CaseStatus.judged || i.score === null) continue;
    judged++;
    scoreSum += i.score;
    if (i.score >= 4) good++; else if (i.score >= 2) mid++; else bad++;
  }
  el<HTMLProgressElement>("eval-live-progress").max = Math.max(total, 1);
  el<HTMLProgressElement>("eval-live-progress").value = cases.size;
  const parts = [`${cases.size} / ${total} answered`, `${judged} judged${failed ? ` · ${failed} failed` : ""}`];
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
    el<HTMLButtonElement>("eval-live-cancel").hidden = run.status !== RunStatus.running;
    el("eval-live-error").textContent = run.error ? `Error: ${run.error}` : "";
    const started = new Date(run.created_at).getTime();
    const until = run.finished_at ? new Date(run.finished_at).getTime() : Date.now();
    const secs = Math.max(0, Math.floor((until - started) / 1000));
    el("eval-live-elapsed").textContent = Number.isNaN(started) ? "" : secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${String(secs % 60).padStart(2, "0")}s`;
  }
}

function setRun(r: EvalRun): void {
  const first = run === undefined || run.num_examples !== r.num_examples;
  run = r;
  if (first) { // one square per question, to see the whole run at a glance
    const strip = el("eval-live-strip");
    strip.replaceChildren();
    squares = [];
    for (let k = 0; k < r.num_examples; k++) {
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
    for (const i of cases.values()) squares[i.idx]?.classList.replace("ev-sq-todo", `ev-sq-${verdictOf(i)}`);
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
  const word = kind === "audits" ? "audit" : "eval";
  el("eval-live-cancel").textContent = `Cancel ${word}`;
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
  const on = (type: FeedEvent["type"], fn: (e: FeedEvent & { run?: EvalRun }) => void): void =>
    es.addEventListener(type, (m) => {
      const e = JSON.parse((m as MessageEvent<string>).data) as FeedEvent;
      fn({ ...e, run: kind === "audits" ? e.audit : e.eval });
    });
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
    // answers the judge never scored (a cancelled or failed eval): redraw them as "not judged"
    for (const i of cases.values()) {
      if (i.status !== CaseStatus.answered) continue;
      const card = buildCard(i);
      cards.get(i.idx)?.replaceWith(card);
      cards.set(i.idx, card);
    }
    conn.textContent = e.run?.status === RunStatus.completed ? "Finished." : `Ended (${e.run?.status ?? "unknown"}).`;
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
