import type { WebSearch, WebSearchTotals } from "./websearch.js";

// Types and small helpers shared by the evals view (evals.ts) and the case modal (case_modal.ts).

// The words of the model (internal/evals/core): Status is the lifecycle of a run or case run, Verdict the judgement of a
// completed case run (empty when it has none). They are shown as they come.
export type Status = "pending" | "running" | "completed" | "errored" | "cancelled";
export type Verdict = "pass" | "fail";
// core.Counts: every case run is in exactly one of pending, running, completed, errored, cancelled; passed and failed split the
// completed ones that have a verdict.
export interface Counts { total: number; pending: number; running: number; completed: number; errored: number; cancelled: number; passed: number; failed: number }
export interface Run {
  id: number; experiment_id: number; status: Status; error?: string; params?: Record<string, unknown>;
  counts: Counts; cost_usd: number; started_at: string; finished_at?: string; case_runs?: CaseRun[];
}
export interface Trajectory {
  seq: number; kind: string; trajectory_id: string; agent_name: string; steps: number; cost_usd: number;
  artifacts?: Artifact[];
}
export interface Artifact { id: number; kind: string; text: string; valid_at: string; created_at: string }
// Says of a case run that it is a rerun with other models and/or thinking levels (empty: the stages kept the ones they had).
export interface RerunInfo { of: number; remember_model?: string; query_model?: string; remember_reasoning?: string; query_reasoning?: string }
// One rerun in the list of the reruns of a case run: the case run it made, what it repeated, the models, the thinking levels and how it ended.
export interface RerunItem {
  case_run_id: number; of: number; remember_model?: string; query_model?: string; remember_reasoning?: string; query_reasoning?: string;
  status: Status; verdict?: Verdict; cost_usd: number;
}
// The outcome of one case of an eval in one run.
export interface CaseRun {
  id: number; eval: string; name: string; status: Status; verdict?: Verdict; score?: number; answer?: string; problems?: string[]; passed_checks?: string[]; dataset_id?: number; error?: string;
  steps: number; cost_usd: number; started_at: string; finished_at: string; trajectories?: Trajectory[]; rerun?: RerunInfo;
}
// The web searches the remember calls of one result (case) made (see websearch.ts), read from its trajectories.
export interface EvalWebSearches { case_run_id: number; totals: WebSearchTotals; searches: WebSearch[] }
// An LLM commentary on a failed case run (core.CaseCommentary).
export interface CaseCommentary {
  id: number; model: string; text: string; input_tokens: number; output_tokens: number; cost_usd: number; created_at: string;
}
// What GET api/evals/case_runs/{id}/debug returns: everything about one case run in one place.
export interface CaseDebug {
  case_run_id: number; eval: string; name: string; status: Status; verdict?: Verdict; error?: string; warnings?: string[]; answer?: string;
  problems: string[]; passed_checks: string[];
  input: { remember?: { kind?: string; text?: string; text_file?: string }[]; query?: string };
  expected?: Record<string, unknown>; params?: Record<string, unknown>; dataset_id?: number;
  commentable: boolean; commentary_enabled: boolean; default_model: string; commentaries: CaseCommentary[];
  cost_usd: number; started_at: string; finished_at?: string;
  // Set when this case run is itself a rerun; rerun_options when the server can rerun the case (the models a rerun can pick).
  rerun?: RerunInfo; rerun_options?: { models: string[]; reasoning: string[] };
  // The reruns of this case run (and of those reruns), oldest first.
  reruns?: RerunItem[];
}
// What POST api/evals/case_runs/{id}/rerun answers: the new case run, already recorded (running).
export interface RerunStarted { case_run_id: number; eval_run_id: number; experiment_run_id?: number }

export function $<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

// Creates an element with optional class and text (textContent only: never inject server data as HTML).
export function h<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

export async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
  return (await res.json()) as T;
}

export const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));

export function fmtTime(t: string | undefined): string {
  if (!t) return "–";
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? t : d.toISOString().replace("T", " ").slice(0, 19);
}

export function fmtDuration(from: string | undefined, to: string | undefined): string {
  if (!from || !to) return "–";
  const ms = new Date(to).getTime() - new Date(from).getTime();
  if (Number.isNaN(ms) || ms < 0) return "–";
  if (ms < 1000) return `${ms} ms`;
  const s = ms / 1000;
  return s < 60 ? `${s.toFixed(1)} s` : `${Math.floor(s / 60)} min ${Math.round(s % 60)} s`;
}

export const fmtCost = (usd: number): string => `$${usd.toFixed(4)}`;

// The badge of a status as the model has it: completed is ok, errored an error, running and pending are active, cancelled is muted.
export function statusBadge(status: string): HTMLElement {
  const cls = status === "completed" ? "badge-ok" : status === "errored" ? "badge-error" : status === "cancelled" ? "badge-unknown" : "badge-active";
  return h("span", `badge ${cls}`, status);
}

// The badge of a verdict (pass or fail); "–" when the case run has none.
export function verdictBadge(verdict: string | undefined): HTMLElement {
  if (!verdict) return h("span", "muted", "–");
  return h("span", `badge ${verdict === "pass" ? "badge-ok" : "badge-error"}`, verdict);
}

// The counts of a run in the model's words, leaving out the zeros: "3 pass · 1 fail · 1 errored of 5".
export function countsLine(c: Counts): string {
  const parts: [number, string][] = [
    [c.passed, "pass"], [c.failed, "fail"], [c.completed - c.passed - c.failed, "no verdict"], [c.errored, "errored"], [c.cancelled, "cancelled"],
    [c.running, "running"], [c.pending, "pending"],
  ];
  const shown = parts.filter(([n]) => n > 0).map(([n, w]) => `${n} ${w}`);
  return `${shown.length ? shown.join(" · ") : "no case runs"} of ${c.total}`;
}

// The statuses of the runs of an experiment as "3 completed · 1 errored".
export function statusLine(by: Partial<Record<Status, number>>): string {
  const parts = Object.entries(by).filter(([, n]) => (n ?? 0) > 0).map(([s, n]) => `${n} ${s}`);
  return parts.length ? parts.join(" · ") : "–";
}

export function numCell(row: HTMLTableRowElement, text: string): void {
  row.insertCell().textContent = text;
  row.cells[row.cells.length - 1].className = "num";
}

export function table(columns: [string, boolean][]): { table: HTMLTableElement; body: HTMLTableSectionElement } {
  const t = h("table");
  const head = t.createTHead().insertRow();
  for (const [label, num] of columns) head.appendChild(h("th", num ? "num" : undefined, label));
  return { table: t, body: t.createTBody() };
}

export function stat(label: string, value: string): HTMLElement {
  const d = h("div");
  d.append(h("dt", undefined, label), h("dd", undefined, value));
  return d;
}

export function props(rows: [string, string | undefined][]): HTMLElement {
  const dl = h("dl", "props");
  for (const [k, v] of rows) {
    if (!v) continue;
    dl.append(h("dt", undefined, k), h("dd", undefined, v));
  }
  return dl;
}

//---------------------------------------------------------------------------------------------
