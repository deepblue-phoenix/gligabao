// The "research" view (see docs/research.md#admin-ui): the deep-research agents. A green "start agent" button (a modal for the task), a
// block on the right of the screen for each working agent (green or red by its health), how many work now, the active agents (each with a
// stop button), the agents that ended (completed, failed or stopped), and for each agent, in a modal, its sub-agents, states and
// report, the error it failed with, and its provenance: what it put in the knowledge base (artifacts, entities, facts).

import { confirmDialog } from "./dialogs.js";
import { agoText, durationText } from "./format.js";
import { showGraph, showGraphFrom } from "./graph.js";
import { openTemporalAt } from "./temporal.js";
import { onViewShown } from "./views.js";

interface State { seq: number; state: string; step: number; detail: string; error: string; started_at: string; finished_at: string | null }
interface Agent {
  id: number; user_id: string; parent_id: number | null; name: string; reason: string; topic: string;
  status: "running" | "completed" | "failed" | "stopped"; state: string; model: string; max_steps: number; runner: string;
  steps: number; tool_calls: number; input_tokens: number; output_tokens: number; cost_usd: number;
  report: string; error: string; created_at: string; finished_at: string | null;
  children: number; duration_ms: number; age_ms: number; has_report: boolean; states?: State[];
}
interface Detail extends Agent { parent: Agent | null; subagents: Agent[] }
interface Counts { running: number; completed: number; failed: number; stopped: number; total: number }
interface ListResponse { counts: Counts; runs: Agent[]; total: number; limit: number }

interface PArtifact { id: number; dataset_id: number; kind: string; text: string; query?: string; parent_id?: number; run_id: number; created_at: string }
interface PEntity { id: number; dataset_id: number; name: string; type: string; description?: string; run_ids: number[] }
interface PEdge {
  id: number; dataset_id: number; source_id: number; target_id: number; source: string; target: string; relation: string; description?: string;
  valid_at?: string; invalid_at?: string; expired_at?: string; created: boolean; run_ids: number[];
}
interface PDataset { id: number; user_id: string; name: string; deleted: boolean; artifacts: number; entities: number; edges: number }
interface PAgent { run_id: number; name?: string; session_id: string }
interface Provenance { run_id: number; include_subagents: boolean; agents: PAgent[]; datasets: PDataset[]; artifacts: PArtifact[]; entities: PEntity[]; edges: PEdge[] }

const PAGE = 50;
const REFRESH_MS = 5000;

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

const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));
const fmtCost = (usd: number): string => `$${usd.toFixed(4)}`;
const fmtInt = (n: number): string => n.toLocaleString("en-US");

async function getJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init);
  if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
  return (await res.json()) as T;
}

function headRow(table: HTMLTableElement, cols: [string, boolean][]): void {
  const head = table.createTHead().insertRow();
  for (const [label, num] of cols) head.appendChild(node("th", num ? "num" : "", label));
}

function numCell(tr: HTMLTableRowElement, text: string): HTMLTableCellElement {
  const td = tr.insertCell();
  td.className = "num";
  td.textContent = text;
  return td;
}

function button(label: string, title: string, cls = "small", onClick?: () => void): HTMLButtonElement {
  const b = node("button", cls, label);
  b.type = "button";
  b.title = title;
  b.setAttribute("aria-label", title);
  if (onClick) b.addEventListener("click", onClick);
  return b;
}

const STATUS_CLASS: Record<Agent["status"], string> = { running: "badge-active", completed: "badge-ok", failed: "badge-error", stopped: "badge-unknown" };

// The agents on screen, so that the running times tick between refreshes: every duration cell of a running agent holds the agent's
// duration when it was fetched and the time of the fetch.
interface Tick { cell: HTMLElement; baseMS: number; at: number }
let ticks: Tick[] = [];

function durationCell(tr: HTMLTableRowElement, a: Agent, at: number): void {
  const td = numCell(tr, durationText(a.duration_ms));
  if (a.status === "running") ticks.push({ cell: td, baseMS: a.duration_ms, at });
}

function startedCell(tr: HTMLTableRowElement, a: Agent): void {
  const td = tr.insertCell();
  td.textContent = agoText(a.age_ms);
  td.title = a.created_at;
}

function agentCell(tr: HTMLTableRowElement, a: Agent, open: (id: number) => void): void {
  const td = tr.insertCell();
  const link = td.appendChild(node("a", "rs-topic", a.topic.length > 120 ? a.topic.slice(0, 120) + "…" : a.topic));
  link.href = "#research";
  link.title = a.topic;
  link.addEventListener("click", (e) => { e.preventDefault(); open(a.id); });
  if (a.parent_id !== null) {
    td.appendChild(node("div", "muted rs-sub", `sub-agent "${a.name}" of #${a.parent_id}`));
  }
}

function statusCell(tr: HTMLTableRowElement, a: Agent): void {
  const td = tr.insertCell();
  if (a.status === "failed") {
    // The error button: click it to see what the agent failed with.
    const b = button("failed ⓘ", "Show the error this agent failed with", "small danger", () => void showError(a.id));
    td.appendChild(b);
    return;
  }
  const badge = td.appendChild(node("span", `badge ${STATUS_CLASS[a.status]}`, a.status));
  if (a.status === "stopped") badge.title = a.error;
  if (a.status === "running") td.appendChild(node("span", "muted rs-state", ` ${a.state}`));
}

async function stopAgent(a: Agent): Promise<void> {
  const kids = a.children > 0 ? ` Its ${a.children} sub-agent${a.children === 1 ? "" : "s"} will be stopped with it.` : "";
  const ok = await confirmDialog(`Stop agent #${a.id} ("${a.topic.slice(0, 80)}")? It ends at once; no report is written.${kids}`, { title: "Stop agent", okLabel: "Stop", danger: true });
  if (!ok) return;
  try {
    await getJSON<Agent>(`api/research/${a.id}/stop`, { method: "POST" });
  } catch (err) {
    el("research-status").className = "error";
    el("research-status").textContent = `Failed to stop agent #${a.id}: ${errText(err)}`;
  }
  await load();
  if (el<HTMLDialogElement>("research-modal").open && currentID === a.id) await showDetail(a.id);
}

function actionsCell(tr: HTMLTableRowElement, a: Agent): void {
  const td = tr.insertCell();
  td.className = "rs-actions";
  td.appendChild(button("Details", "Details, sub-agents, states and report", "small", () => void showDetail(a.id)));
  td.appendChild(button("Knowledge", "What this agent put in the knowledge base", "small", () => void showProvenance(a.id, a.children > 0)));
  if (a.status === "running") td.appendChild(button("Stop", "Stop this agent", "small danger", () => void stopAgent(a)));
}

// ----- the table of the agents

interface Panel { table: HTMLTableElement; body: HTMLTableSectionElement; note: HTMLElement }

function makeTable(box: HTMLElement, cols: [string, boolean][]): Panel {
  const note = box.appendChild(node("p", "muted"));
  const scroll = box.appendChild(node("div", "table-scroll"));
  const table = scroll.appendChild(node("table", "rs-table"));
  table.hidden = true;
  headRow(table, cols);
  return { table, body: table.createTBody(), note };
}

const COMMON_HEAD: [string, boolean][] = [["ID", true], ["User", false], ["Agent", false], ["Status", false], ["Children", true], ["Running time", true], ["Started", false], ["Steps", true], ["Cost", true], ["", false]];

let active: Panel;
let ended: Panel;
let finishedFilter = "finished";
let finishedLimit = PAGE;
let currentID = 0;
let currentRunning = false;
let timer: number | undefined;

function fill(p: Panel, runs: Agent[], empty: string, at: number): void {
  p.body.replaceChildren();
  p.table.hidden = runs.length === 0;
  p.note.textContent = runs.length === 0 ? empty : "";
  for (const a of runs) {
    const tr = p.body.insertRow();
    numCell(tr, String(a.id));
    tr.insertCell().textContent = a.user_id;
    agentCell(tr, a, (id) => void showDetail(id));
    statusCell(tr, a);
    numCell(tr, String(a.children));
    durationCell(tr, a, at);
    startedCell(tr, a);
    numCell(tr, `${a.steps}/${a.max_steps}`).title = `${a.tool_calls} tool calls, ${fmtInt(a.input_tokens)} input and ${fmtInt(a.output_tokens)} output tokens`;
    numCell(tr, fmtCost(a.cost_usd));
    actionsCell(tr, a);
  }
}

function tile(label: string, value: number, cls = ""): HTMLElement {
  const d = node("div", cls);
  d.append(node("dt", "", label), node("dd", "", String(value)));
  return d;
}

async function load(): Promise<void> {
  const status = el("research-status");
  try {
    const at = Date.now();
    const [act, fin] = await Promise.all([
      getJSON<ListResponse>("api/research?status=running&limit=500"),
      getJSON<ListResponse>(`api/research?status=${finishedFilter}&limit=${finishedLimit}`),
    ]);
    status.className = "muted";
    status.textContent = "";
    const c = act.counts;
    el("research-stats").replaceChildren(tile("Running now", c.running, "rs-running"), tile("Completed", c.completed), tile("Failed", c.failed), tile("Stopped", c.stopped), tile("Total", c.total));
    ticks = [];
    fill(active, act.runs, "No agent is working now.", at);
    fill(ended, fin.runs, "No agent has ended yet.", at);
    await updateDock(act.runs);
    const more = el<HTMLButtonElement>("research-more");
    more.hidden = fin.runs.length >= fin.total || fin.runs.length < finishedLimit;
    el("research-ended-count").textContent = `${fin.runs.length} of ${fin.total}`;
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load the research agents: ${errText(err)}`;
    renderDock(true);
  }
}

function tickTimes(): void {
  const now = Date.now();
  for (const t of [...ticks, ...dockTicks]) t.cell.textContent = durationText(t.baseMS + (now - t.at));
}

function isViewShown(): boolean {
  return !el("view-research").hidden;
}

// ----- the error of a failed agent

async function showError(id: number): Promise<void> {
  const modal = el<HTMLDialogElement>("research-error-modal");
  const body = el("research-error-body");
  el("research-error-title").textContent = `Agent #${id} failed`;
  body.replaceChildren(node("p", "muted", "Loading…"));
  if (!modal.open) modal.showModal();
  try {
    const d = await getJSON<Detail>(`api/research/${id}`);
    body.replaceChildren();
    const info = body.appendChild(node("dl", "stats rs-info"));
    const row = (k: string, v: string) => { const div = info.appendChild(node("div")); div.append(node("dt", "", k), node("dd", "", v)); };
    row("Agent", d.name ? `#${d.id} (sub-agent "${d.name}")` : `#${d.id}`);
    row("User", d.user_id);
    row("Ran for", durationText(d.duration_ms));
    // The state it failed in is the one before the terminal "failed" one.
    const failedIn = (d.states ?? []).filter((s) => s.state !== "failed").pop();
    if (failedIn) row("Failed during", `${failedIn.state}${failedIn.detail ? `: ${failedIn.detail}` : ""} (step ${failedIn.step})`);
    body.appendChild(node("p", "", `Topic: ${d.topic}`));
    body.appendChild(node("h4", "", "Error"));
    body.appendChild(node("pre", "rs-error", d.error || "(no error text was recorded)"));
    const open = body.appendChild(button("Open agent", "Open the details of this agent", "small", () => { modal.close(); void showDetail(id); }));
    open.classList.add("rs-open");
  } catch (err) {
    body.replaceChildren(node("p", "error", `Failed to load agent #${id}: ${errText(err)}`));
  }
}

// ----- the details of an agent

let renderedID = 0; // the agent whose details the modal shows

async function showDetail(id: number): Promise<void> {
  const modal = el<HTMLDialogElement>("research-modal");
  const body = el("research-body");
  currentID = id;
  el("research-title").textContent = `Agent #${id}`;
  el("research-actions").replaceChildren();
  if (!modal.open) {
    body.replaceChildren(node("p", "muted", "Loading…"));
    modal.showModal();
  }
  let d: Detail;
  try {
    d = await getJSON<Detail>(`api/research/${id}`);
  } catch (err) {
    body.replaceChildren(node("p", "error", `Failed to load agent #${id}: ${errText(err)}`));
    return;
  }
  if (currentID !== id) return; // another agent was opened meanwhile
  currentRunning = d.status === "running";
  el("research-title").textContent = `Agent #${d.id}${d.name ? ` · ${d.name}` : ""}`;
  const actions = el("research-actions");
  actions.replaceChildren();
  actions.append(node("span", `badge ${STATUS_CLASS[d.status]}`, d.status));
  actions.append(button("Knowledge", "What this agent put in the knowledge base", "small", () => void showProvenance(d.id, d.children > 0)));
  actions.append(button("Temporal plot", "Plot what this agent did to the facts on a time axis: established, invalidated, still true", "small", () => {
    openTemporalAt(`Agent #${d.id} · temporal changes`, `api/research/${d.id}/temporal?include_subagents=${d.children > 0}`);
  }));
  if (d.status === "running") actions.append(button("Stop", "Stop this agent", "small danger", () => void stopAgent(d)));
  // A refresh redraws the whole body: keep what the user opened and where they scrolled.
  const same = renderedID === id;
  const wasOpen = same && (body.querySelector<HTMLDetailsElement>("details.rs-states")?.open ?? false);
  const scroll = same ? body.scrollTop : 0;
  renderedID = id;
  const fresh = renderDetail(d);
  fresh.querySelector<HTMLDetailsElement>("details.rs-states")!.open = wasOpen;
  body.replaceChildren(fresh);
  body.scrollTop = scroll;
}

function renderDetail(d: Detail): HTMLElement {
  const root = node("div", "rs-detail");
  const info = root.appendChild(node("dl", "stats rs-info"));
  const row = (k: string, v: string, title = "") => { const div = info.appendChild(node("div")); if (title) div.title = title; div.append(node("dt", "", k), node("dd", "", v)); };
  row("User", d.user_id);
  row("Children", String(d.children), "How many sub-agents it spawned");
  row("Running time", durationText(d.duration_ms), d.finished_at ? "From its start to its end" : "Working now");
  row("Started", agoText(d.age_ms), d.created_at);
  row("Steps", `${d.steps} of ${d.max_steps}`);
  row("Tool calls", String(d.tool_calls));
  row("Tokens", `${fmtInt(d.input_tokens)} in / ${fmtInt(d.output_tokens)} out`);
  row("Cost", fmtCost(d.cost_usd));
  row("Model", d.model || "(server default)");
  row("Runs in", d.runner === "modal" ? "Modal (gligabao_agent binary)" : "this server", "Where the agent runs: in the server's process, or in a Modal container started through the sidecar");
  row("State", d.state);

  root.appendChild(node("h4", "", "Topic"));
  root.appendChild(node("p", "rs-text", d.topic));
  if (d.reason) {
    root.appendChild(node("h4", "", "Why it was spawned"));
    root.appendChild(node("p", "rs-text", d.reason));
  }
  if (d.parent) {
    const p = root.appendChild(node("p", "muted", "Sub-agent of "));
    const a = p.appendChild(node("a", "", `#${d.parent.id}`));
    a.href = "#research";
    a.addEventListener("click", (e) => { e.preventDefault(); void showDetail(d.parent!.id); });
    p.append(` (${d.parent.topic.slice(0, 80)})`);
  }
  if (d.status === "failed") {
    root.appendChild(node("h4", "", "Error"));
    root.appendChild(node("pre", "rs-error", d.error));
  } else if (d.status === "stopped") {
    root.appendChild(node("p", "muted", `Stopped: ${d.error}`));
  }

  // The sub-agents: whether it spawned any.
  root.appendChild(node("h4", "", `Sub-agents (${d.subagents.length})`));
  if (d.subagents.length === 0) {
    root.appendChild(node("p", "muted", "This agent did not spawn any sub-agent."));
  } else {
    const table = root.appendChild(node("table", "rs-table"));
    headRow(table, [["ID", true], ["Name", false], ["Reason", false], ["Status", false], ["Children", true], ["Running time", true], ["Started", false], ["", false]]);
    const body = table.createTBody();
    const at = Date.now();
    for (const k of d.subagents) {
      const tr = body.insertRow();
      numCell(tr, String(k.id));
      tr.insertCell().textContent = k.name;
      const why = tr.insertCell();
      why.textContent = k.reason.length > 100 ? k.reason.slice(0, 100) + "…" : k.reason;
      why.title = k.reason;
      statusCell(tr, k);
      numCell(tr, String(k.children));
      durationCell(tr, k, at);
      startedCell(tr, k);
      const td = tr.insertCell();
      td.className = "rs-actions";
      td.appendChild(button("Open", "Open this sub-agent", "small", () => void showDetail(k.id)));
      if (k.status === "running") td.appendChild(button("Stop", "Stop this sub-agent", "small danger", () => void stopAgent(k)));
    }
  }

  if (d.report) {
    root.appendChild(node("h4", "", "Report"));
    root.appendChild(node("pre", "rs-report", d.report));
  }

  const states = d.states ?? [];
  const det = root.appendChild(node("details", "rs-states"));
  det.appendChild(node("summary", "", `States (${states.length})`));
  const table = det.appendChild(node("table", "rs-table compact"));
  headRow(table, [["#", true], ["State", false], ["Step", true], ["Detail", false], ["Time", true], ["Error", false]]);
  const body = table.createTBody();
  for (const s of states) {
    const tr = body.insertRow();
    numCell(tr, String(s.seq));
    tr.insertCell().textContent = s.state;
    numCell(tr, String(s.step));
    tr.insertCell().textContent = s.detail;
    const ms = s.finished_at ? Date.parse(s.finished_at) - Date.parse(s.started_at) : Date.now() - Date.parse(s.started_at);
    numCell(tr, ms < 1000 ? `${Math.max(0, Math.round(ms))} ms` : durationText(ms));
    const e = tr.insertCell();
    e.textContent = s.error;
    if (s.error) e.className = "error";
  }
  return root;
}

// ----- provenance: what an agent put in the knowledge base

let provID = 0;

async function showProvenance(id: number, withKids: boolean): Promise<void> {
  const modal = el<HTMLDialogElement>("research-prov-modal");
  provID = id;
  el("research-prov-title").textContent = `Agent #${id} · knowledge`;
  const box = el<HTMLInputElement>("research-prov-kids");
  box.checked = withKids;
  box.onchange = () => void showProvenance(id, box.checked);
  const body = el("research-prov-body");
  body.replaceChildren(node("p", "muted", "Loading…"));
  if (!modal.open) modal.showModal();
  let p: Provenance;
  try {
    p = await getJSON<Provenance>(`api/research/${id}/provenance?include_subagents=${withKids}`);
  } catch (err) {
    body.replaceChildren(node("p", "error", `Failed to load the knowledge of agent #${id}: ${errText(err)}`));
    return;
  }
  if (provID !== id) return;
  body.replaceChildren(renderProvenance(p));
}

function renderProvenance(p: Provenance): HTMLElement {
  const root = node("div", "rs-prov");
  const dsName = new Map(p.datasets.map((d) => [d.id, `${d.user_id} / ${d.name}${d.deleted ? " (deleted)" : ""}`]));
  const ds = (id: number): string => dsName.get(id) ?? `dataset ${id}`;
  const agents = new Map(p.agents.map((a) => [a.run_id, a.name ? `#${a.run_id} ${a.name}` : `#${a.run_id}`]));
  const who = (ids: number[]): string => ids.map((i) => agents.get(i) ?? `#${i}`).join(", ");

  const stats = root.appendChild(node("dl", "stats"));
  const t = (k: string, v: number) => { const d = stats.appendChild(node("div")); d.append(node("dt", "", k), node("dd", "", String(v))); };
  t("Artifacts", p.artifacts.length);
  t("Entities", p.entities.length);
  t("Facts", p.edges.length);
  t("Datasets", p.datasets.length);
  const sess = p.agents.map((a) => a.session_id).join(", ");
  root.appendChild(node("p", "muted", `Read from the session${p.agents.length === 1 ? "" : "s"} ${sess}: everything the agent's remember tool stored carries it. ` +
    "An entity or fact is listed also when the agent only found it again after another source had stored it (a fact the agent created is marked created)."));
  if (p.artifacts.length + p.entities.length + p.edges.length === 0) {
    root.appendChild(node("p", "muted", "This agent put nothing in the knowledge base (it did not use remember, or what it queued is still being absorbed)."));
    return root;
  }

  const dsTable = root.appendChild(node("table", "rs-table compact"));
  headRow(dsTable, [["Dataset", false], ["Artifacts", true], ["Entities", true], ["Facts", true], ["", false]]);
  const dsBody = dsTable.createTBody();
  for (const d of p.datasets) {
    const tr = dsBody.insertRow();
    tr.insertCell().textContent = ds(d.id);
    numCell(tr, String(d.artifacts));
    numCell(tr, String(d.entities));
    numCell(tr, String(d.edges));
    const td = tr.insertCell();
    if (!d.deleted) td.appendChild(button("Dataset graph", "Open the whole graph of this dataset", "small", () => void showGraph("standard", d.user_id, d.name)));
  }
  const gbtn = root.appendChild(button("Graph of what this agent contributed", "Show only the entities and facts of this agent", "small rs-graph-btn", () => void showGraphFrom(
    `Agent #${p.run_id} · what it contributed`, async () => provenanceGraph(p))));
  gbtn.disabled = p.edges.length + p.entities.length === 0;

  const section = (title: string, n: number): HTMLElement => {
    const det = root.appendChild(node("details", "rs-states"));
    det.open = true;
    det.appendChild(node("summary", "", `${title} (${n})`));
    return det;
  };
  if (p.artifacts.length > 0) {
    const det = section("Artifacts", p.artifacts.length);
    const table = det.appendChild(node("table", "rs-table compact"));
    headRow(table, [["ID", true], ["Kind", false], ["Agent", false], ["Dataset", false], ["Stored", false], ["Text", false]]);
    const body = table.createTBody();
    for (const a of p.artifacts) {
      const tr = body.insertRow();
      numCell(tr, String(a.id));
      tr.insertCell().textContent = a.kind;
      tr.insertCell().textContent = who([a.run_id]);
      tr.insertCell().textContent = ds(a.dataset_id);
      const when = tr.insertCell();
      when.textContent = a.created_at.replace("T", " ").slice(0, 19);
      tr.insertCell().textContent = a.query ? `[search: ${a.query}] ${a.text}` : a.text;
      tr.cells[5].className = "rs-wrap";
    }
  }
  if (p.entities.length > 0) {
    const det = section("Entities", p.entities.length);
    const table = det.appendChild(node("table", "rs-table compact"));
    headRow(table, [["ID", true], ["Name", false], ["Type", false], ["Description", false], ["Agent", false], ["Dataset", false]]);
    const body = table.createTBody();
    for (const e of p.entities) {
      const tr = body.insertRow();
      numCell(tr, String(e.id));
      tr.insertCell().textContent = e.name;
      tr.insertCell().textContent = e.type;
      tr.insertCell().textContent = e.description ?? "";
      tr.cells[3].className = "rs-wrap";
      tr.insertCell().textContent = who(e.run_ids);
      tr.insertCell().textContent = ds(e.dataset_id);
    }
  }
  if (p.edges.length > 0) {
    const det = section("Facts (edges)", p.edges.length);
    const table = det.appendChild(node("table", "rs-table compact"));
    headRow(table, [["ID", true], ["Fact", false], ["Description", false], ["", false], ["Valid", false], ["Agent", false], ["Dataset", false]]);
    const body = table.createTBody();
    for (const e of p.edges) {
      const tr = body.insertRow();
      numCell(tr, String(e.id));
      tr.insertCell().textContent = `${e.source} —${e.relation}→ ${e.target}`;
      tr.insertCell().textContent = e.description ?? "";
      tr.cells[2].className = "rs-wrap";
      const c = tr.insertCell();
      c.textContent = e.created ? "created" : "restated";
      c.title = e.created ? "An artifact of this agent created the fact" : "The fact was stored before; this agent stated it again";
      const valid = tr.insertCell();
      valid.textContent = `${(e.valid_at ?? "").slice(0, 10)}${e.invalid_at ? ` → ${e.invalid_at.slice(0, 10)}` : ""}${e.expired_at ? " (expired)" : ""}`;
      tr.insertCell().textContent = who(e.run_ids);
      tr.insertCell().textContent = ds(e.dataset_id);
    }
  }
  return root;
}

// The entities and facts of a provenance as a graph for the graph modal. An end of a fact that the agent did not list as an entity is
// added from the fact's own names.
function provenanceGraph(p: Provenance) {
  const entities = new Map<number, { id: number; name: string; type: string; description?: string }>();
  for (const e of p.entities) entities.set(e.id, { id: e.id, name: e.name, type: e.type, description: e.description });
  for (const e of p.edges) {
    if (!entities.has(e.source_id)) entities.set(e.source_id, { id: e.source_id, name: e.source, type: "" });
    if (!entities.has(e.target_id)) entities.set(e.target_id, { id: e.target_id, name: e.target, type: "" });
  }
  return {
    dataset: `agent ${p.run_id}`,
    entities: [...entities.values()],
    edges: p.edges.map((e) => ({
      id: e.id, source_id: e.source_id, target_id: e.target_id, relation: e.relation, description: e.description,
      valid_at: e.valid_at, invalid_at: e.invalid_at, expired_at: e.expired_at,
    })),
  };
}

// ----- starting an agent

async function startAgent(): Promise<void> {
  const topic = el<HTMLTextAreaElement>("research-start-topic");
  const err = el("research-start-error");
  const submit = el<HTMLButtonElement>("research-start-submit");
  const text = topic.value.trim();
  const runner = el<HTMLSelectElement>("research-start-runner").value;
  if (!text) {
    err.textContent = "Describe the research task first.";
    err.hidden = false;
    topic.focus();
    return;
  }
  submit.disabled = true;
  err.hidden = true;
  try {
    const a = await getJSON<Agent>("api/research", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ topic: text, runner }) });
    el<HTMLDialogElement>("research-start-modal").close();
    topic.value = "";
    dock.set(a.id, a);
    renderDock(false);
    await load();
  } catch (e) {
    err.textContent = `Failed to start the agent: ${errText(e)}`;
    err.hidden = false;
  } finally {
    submit.disabled = false;
  }
}

// ----- the dock: a block on the right of the screen for each agent that works (and for each that ended while it was shown, until it is
// dismissed). Its background is green while the agent is healthy (working or completed) and red when it failed, was stopped, or the server
// cannot be reached.

const dock = new Map<number, Agent>();
const dockTicks: Tick[] = [];

function isHealthy(a: Agent): boolean {
  return a.status === "running" || a.status === "completed";
}

function renderDock(unreachable: boolean): void {
  const box = el("research-dock");
  box.replaceChildren();
  dockTicks.length = 0;
  box.hidden = dock.size === 0;
  const at = Date.now();
  for (const a of dock.values()) {
    const bad = unreachable && a.status === "running" || !isHealthy(a);
    const b = box.appendChild(node("div", `rs-agent ${bad ? "rs-bad" : "rs-ok"}`));
    b.title = `${a.topic}\n\nClick for the details of the agent`;
    b.tabIndex = 0;
    b.setAttribute("role", "button");
    b.appendChild(node("div", "rs-agent-title", `Agent #${a.id} · ${unreachable && a.status === "running" ? "no connection" : a.status === "running" ? a.state : a.status}`));
    b.appendChild(node("div", "rs-agent-topic", a.topic));
    const info = b.appendChild(node("div", "rs-agent-info"));
    const dur = info.appendChild(node("span", "", durationText(a.duration_ms)));
    info.append(` · step ${a.steps}/${a.max_steps}`);
    if (a.status === "running") dockTicks.push({ cell: dur, baseMS: a.duration_ms, at });
    b.addEventListener("click", () => void showDetail(a.id));
    b.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); void showDetail(a.id); } });
    if (a.status !== "running") {
      const x = b.appendChild(button("×", "Dismiss", "rs-agent-x", () => { dock.delete(a.id); renderDock(unreachable); }));
      x.addEventListener("click", (e) => e.stopPropagation());
    }
  }
}

// updateDock shows the agents that work now (the researches, not their sub-agents) and looks up the ones that were shown and are not
// working any more, to show how they ended.
async function updateDock(running: Agent[]): Promise<void> {
  const now = new Set<number>();
  for (const a of running) {
    if (a.parent_id !== null) continue;
    now.add(a.id);
    dock.set(a.id, a);
  }
  renderDock(false);
  const gone = [...dock.values()].filter((a) => a.status === "running" && !now.has(a.id));
  if (gone.length === 0) return;
  await Promise.all(gone.map(async (a) => {
    try {
      dock.set(a.id, await getJSON<Agent>(`api/research/${a.id}`));
    } catch {
      // keep it as it was: the next refresh looks again
    }
  }));
  renderDock(false);
}

// ----- the view

function schedule(): void {
  window.clearInterval(timer);
  timer = window.setInterval(() => {
    if (!isViewShown()) return;
    tickTimes();
  }, 1000);
}

export function initResearch(): void {
  const root = el("research-content");
  const act = root.appendChild(node("div", "rs-section"));
  act.appendChild(node("h3", "", "Active agents"));
  active = makeTable(act, COMMON_HEAD);
  const end = root.appendChild(node("div", "rs-section"));
  const head = end.appendChild(node("div", "row rs-ended-head"));
  head.appendChild(node("h3", "", "Stopped and finished agents"));
  const sel = head.appendChild(node("select"));
  for (const [v, label] of [["finished", "all that ended"], ["completed", "completed"], ["failed", "failed"], ["stopped", "stopped"]]) {
    const o = sel.appendChild(node("option", "", label));
    o.value = v;
  }
  sel.addEventListener("change", () => { finishedFilter = sel.value; finishedLimit = PAGE; void load(); });
  head.appendChild(node("span", "muted", "")).id = "research-ended-count";
  ended = makeTable(end, COMMON_HEAD);
  const more = end.appendChild(button("Show more", "Show older agents", "", () => { finishedLimit += PAGE; void load(); }));
  more.id = "research-more";
  more.hidden = true;

  el("research-refresh").addEventListener("click", () => void load());
  const startModal = el<HTMLDialogElement>("research-start-modal");
  el("research-start").addEventListener("click", () => {
    el("research-start-error").hidden = true;
    startModal.showModal();
    el("research-start-topic").focus();
  });
  el("research-start-close").addEventListener("click", () => startModal.close());
  el("research-start-cancel").addEventListener("click", () => startModal.close());
  el("research-start-submit").addEventListener("click", () => void startAgent());
  el("research-start-topic").addEventListener("keydown", (e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) void startAgent(); });
  el("research-detail-close").addEventListener("click", () => el<HTMLDialogElement>("research-modal").close());
  el("research-error-close").addEventListener("click", () => el<HTMLDialogElement>("research-error-modal").close());
  el("research-prov-close").addEventListener("click", () => el<HTMLDialogElement>("research-prov-modal").close());
  el<HTMLDialogElement>("research-modal").addEventListener("close", () => { currentID = 0; renderedID = 0; });
  schedule();
  // The list refreshes itself while the view is shown (and the agents work): on show, then every few seconds.
  onViewShown("research", () => void load());
  window.setInterval(() => {
    if (!isViewShown() || document.hidden) return;
    void load();
    if (el<HTMLDialogElement>("research-modal").open && currentID && currentRunning) void showDetail(currentID);
  }, REFRESH_MS);
  void load();
}
