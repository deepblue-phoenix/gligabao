// Long-running actions of a training backend (e.g. deploying the Modal app) with a live log: the sidecar runs them and
// streams their output as SSE (api/sidecar/backends/<backend>/actions/<action>/events, passed on by the server). The
// dialog #action-modal shows the stages, the state and the log as it grows; closing it leaves the action running.

export interface ActionState {
  action: string;
  label: string;
  confirm: string;   // what to ask before starting it (empty: a default question)
  danger: boolean;   // a destructive action
  run_id: number;
  state: "idle" | "running" | "succeeded" | "failed" | "cancelled";
  stage: string | null;
  stages: string[];
  started_at: number | null;
  finished_at: number | null;
  exit_code: number | null;
  error: string;
  lines: number;
}

interface LogLine { i: number; t: number; line: string }

const MAX_DOM_LINES = 5000;
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

const actionURL = (backend: string, action: string): string => `api/sidecar/backends/${encodeURIComponent(backend)}/actions/${encodeURIComponent(action)}`;

// What the open dialog follows.
let target: { backend: string; action: string } | undefined;
let stream: EventSource | undefined;
let runID = -1;      // run the log on screen belongs to
let nextLine = 0;    // index of the next log line to show (a reconnect replays the log; lines already shown are skipped)
let current: ActionState | undefined;
let ticker: number | undefined;
let onChange: (() => void) | undefined;

function fmtElapsed(s: number): string {
  s = Math.max(0, Math.round(s));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
}

function renderState(st: ActionState): void {
  current = st;
  const badge = el("action-state");
  badge.textContent = st.state;
  badge.className = `badge ${st.state === "succeeded" ? "badge-ok" : st.state === "failed" ? "badge-error" : st.state === "cancelled" ? "badge-unknown" : st.state === "running" ? "badge-active" : "badge-unknown"}`;
  const stages = el("action-stages");
  const at = st.stage ? st.stages.indexOf(st.stage) : -1;
  stages.replaceChildren(...st.stages.map((name, i) => {
    let cls = "pending";
    if (st.state === "succeeded" || i < at) cls = "done";
    else if (i === at) cls = st.state === "failed" || st.state === "cancelled" ? "failed" : st.state === "running" ? "active" : "done";
    return node("li", `stage stage-${cls}`, name);
  }));
  el("action-error").textContent = st.state === "failed" ? `Failed: ${st.error || "see the log"}` : st.state === "cancelled" ? "Cancelled." : "";
  el<HTMLButtonElement>("action-cancel").hidden = st.state !== "running";
  el<HTMLButtonElement>("action-cancel").disabled = false;
  renderElapsed();
  if (st.state === "running" && ticker === undefined) ticker = window.setInterval(renderElapsed, 1000);
  if (st.state !== "running" && ticker !== undefined) { clearInterval(ticker); ticker = undefined; }
}

function renderElapsed(): void {
  const st = current;
  if (!st || st.started_at == null) { el("action-elapsed").textContent = ""; return; }
  el("action-elapsed").textContent = fmtElapsed((st.finished_at ?? Date.now() / 1000) - st.started_at);
}

function appendLines(lines: LogLine[]): void {
  const log = el("action-log");
  const body = log.parentElement as HTMLElement;
  const started = current?.started_at ?? lines[0]?.t ?? 0;
  for (const l of lines) {
    if (l.i < nextLine) continue;
    nextLine = l.i + 1;
    const row = node("div", /error|failed|traceback|exception/i.test(l.line) ? "ll ll-err" : "ll");
    row.append(node("span", "ll-t", `+${Math.max(0, l.t - started).toFixed(1)}s`), l.line);
    log.appendChild(row);
  }
  while (log.childElementCount > MAX_DOM_LINES) log.firstElementChild?.remove();
  if (el<HTMLInputElement>("action-follow").checked) body.scrollTop = body.scrollHeight;
}

function reset(): void {
  runID = -1;
  nextLine = 0;
  current = undefined;
  el("action-log").replaceChildren();
  el("action-stages").replaceChildren();
  el("action-error").textContent = "";
  el("action-conn").textContent = "";
}

function stop(): void {
  stream?.close();
  stream = undefined;
  if (ticker !== undefined) { clearInterval(ticker); ticker = undefined; }
}

// Opens the dialog on the log of `action` of `backend` and follows it until it ends. cb is called when the run ended.
export function showActionLog(backend: string, action: string, title: string, cb?: () => void): void {
  const modal = el<HTMLDialogElement>("action-modal");
  stop();
  reset();
  onChange = cb;
  target = { backend, action };
  el("action-cancel").hidden = true;
  el("action-title").textContent = title;
  el<HTMLInputElement>("action-follow").checked = true;
  if (!modal.open) modal.showModal();

  const es = new EventSource(`${actionURL(backend, action)}/events`);
  stream = es;
  const conn = el("action-conn");
  conn.textContent = "Connecting…";
  es.onopen = () => { conn.textContent = "Live."; };
  es.onerror = () => { conn.textContent = "Connection lost, reconnecting…"; }; // EventSource retries; the replay is deduplicated
  es.addEventListener("log", (e) => appendLines((JSON.parse((e as MessageEvent<string>).data) as { lines: LogLine[] }).lines));
  es.addEventListener("state", (e) => {
    const st = JSON.parse((e as MessageEvent<string>).data) as ActionState;
    if (st.run_id !== runID) { // a new run (or the first state): start from a clean log
      if (runID !== -1) { el("action-log").replaceChildren(); nextLine = 0; }
      runID = st.run_id;
    }
    renderState(st);
    if (st.state === "idle") { conn.textContent = "Not run yet."; stop(); return; }
    if (st.state !== "running") { // over: EventSource would reconnect to the closed stream otherwise
      conn.textContent = st.state === "succeeded" ? "Finished." : st.state === "cancelled" ? "Cancelled." : "Ended.";
      stop();
      onChange?.();
    }
  });
}

// Starts the action (the user already confirmed) and opens its log. If it is already running, only opens the log.
export async function startAction(backend: string, action: string, title: string, cb?: () => void): Promise<void> {
  const res = await fetch(actionURL(backend, action), { method: "POST" });
  if (!res.ok && res.status !== 409) {
    let msg = (await res.text()).trim();
    try { msg = (JSON.parse(msg) as { error?: string }).error ?? msg; } catch { /* not JSON */ }
    throw new Error(`${res.status}: ${msg}`);
  }
  showActionLog(backend, action, title, cb);
  cb?.();
}

export function initActionModal(): void {
  const modal = el<HTMLDialogElement>("action-modal");
  modal.addEventListener("close", stop);
  el("action-close").addEventListener("click", () => modal.close());
  // Cancel stops the command (the sidecar interrupts it, then kills it); the stream then reports the run as cancelled.
  el("action-cancel").addEventListener("click", () => {
    if (!target || !confirm("Cancel the running action? Work already done on Modal is not rolled back.")) return;
    const btn = el<HTMLButtonElement>("action-cancel");
    btn.disabled = true;
    fetch(`${actionURL(target.backend, target.action)}/cancel`, { method: "POST" })
      .then(async (res) => { if (!res.ok && res.status !== 409) throw new Error(`${res.status}: ${(await res.text()).trim()}`); })
      .catch((err) => { btn.disabled = false; alert(`Could not cancel: ${err instanceof Error ? err.message : String(err)}`); });
  });
  // Scrolling up in the log stops following; the checkbox turns it on again.
  const body = el("action-log").parentElement as HTMLElement;
  body.addEventListener("scroll", () => {
    if (body.scrollHeight - body.scrollTop - body.clientHeight > 40) el<HTMLInputElement>("action-follow").checked = false;
  });
  el("action-follow").addEventListener("change", () => {
    if (el<HTMLInputElement>("action-follow").checked) body.scrollTop = body.scrollHeight;
  });
}
