// Web search in the admin UI (see docs/web-search.md): the "Web search" dashboard card (totals per database and the
// datasets that hold web searches) and the modal that lists the searches of an eval or of a dataset, each with its
// result, sources, the entities and facts it yielded and its latency.
//
// Two sources feed it. The database knows the searches that found something (the web_search artifacts and what hangs off
// them); the ATIF trajectories of the evals also know the searches that found nothing, the queries the model sent to the
// search engine, the latency and the cost, so those fields are only there for the searches of an eval.

import { alertDialog } from "./dialogs.js";

export interface WebSource { url: string; title?: string; page_age?: string }
export interface WebEntity { id?: number; name: string; type: string; description?: string; only_web?: boolean }
export interface WebEdge { id?: number; source: string; target: string; relation: string; description?: string; created?: boolean }
export interface WebSearch {
  entity?: string; entity_type?: string; query: string;
  parent_artifact_id?: number; parent_text?: string; artifact_id?: number;
  found: boolean; error?: string; briefing?: string;
  searches?: string[]; billed_searches?: number; search_errors?: string[];
  sources?: WebSource[]; entities?: WebEntity[]; edges?: WebEdge[]; invalidated_edges?: number[];
  started_at?: string; latency_ms?: number; model?: string; prompt_tokens?: number; completion_tokens?: number; cost_usd?: number;
  trajectory_id?: string;
}
export interface Latency { count: number; avg_ms: number; median_ms: number; max_ms: number }
export interface WebSearchTotals {
  entity_searches: number; found: number; empty: number; failed: number; engine_searches: number; sources: number;
  entities: number; edges: number; invalidated_edges: number; cost_usd: number; latency: Latency;
}
interface DatasetSummary { artifacts: number; parents: number; sources: number; entities: number; only_web: number; edges: number; supported_edges: number }
interface DatasetRow extends DatasetSummary { id: number; user_id: string; name: string; deleted: boolean }
interface Stats {
  database: DatasetSummary & { datasets: number };
  datasets: DatasetRow[];
  trajectories?: WebSearchTotals & { calls: number };
}

function el<T extends HTMLElement>(id: string): T {
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

const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));
const plural = (n: number, one: string, many = one + "s"): string => `${n} ${n === 1 ? one : many}`;
const fmtCost = (usd: number): string => `$${usd.toFixed(4)}`;

export function fmtMs(ms: number): string {
  return ms < 1000 ? `${Math.round(ms)} ms` : `${(ms / 1000).toFixed(ms < 10000 ? 2 : 1)} s`;
}

function fmtTime(t: string | undefined): string {
  if (!t) return "";
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? t : d.toISOString().replace("T", " ").slice(0, 19) + " UTC";
}

function stat(label: string, value: string, title?: string): HTMLElement {
  const d = h("div");
  const dt = h("dt", undefined, label);
  if (title) d.title = title;
  d.append(dt, h("dd", undefined, value));
  return d;
}

// Only http(s) links are made clickable: a source URL comes from the web.
function link(url: string, text: string): HTMLElement {
  if (!/^https?:\/\//i.test(url)) return h("span", undefined, text);
  const a = h("a", undefined, text);
  a.href = url;
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  return a;
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
  return (await res.json()) as T;
}

//---------------------------------------------------------------------------------------------
// The totals of a list of searches (what an eval made), one line.

export function totalsLine(t: WebSearchTotals): string {
  const parts = [`${plural(t.entity_searches, "entity search", "entity searches")} (${t.found} found, ${t.empty} empty${t.failed ? `, ${t.failed} failed` : ""})`];
  if (t.engine_searches) parts.push(plural(t.engine_searches, "search-tool search", "search-tool searches"));
  parts.push(plural(t.entities, "entity", "entities"), plural(t.edges, "fact"));
  if (t.latency.count) parts.push(`latency avg ${fmtMs(t.latency.avg_ms)}, max ${fmtMs(t.latency.max_ms)}`);
  if (t.cost_usd) parts.push(fmtCost(t.cost_usd));
  return parts.join(" · ");
}

// The stat tiles of the totals of searches read from trajectories.
function trajectoryStats(t: WebSearchTotals): HTMLElement[] {
  const out = [
    stat("Entity searches", String(t.entity_searches), "Web searches made, one per key entity of a remembered text"),
    stat("Found / empty / failed", `${t.found} / ${t.empty} / ${t.failed}`),
    stat("Search-tool searches", String(t.engine_searches), "Searches the model made with the web search tool (billed: $0.01 each)"),
    stat("Sources cited", String(t.sources)),
    stat("Entities extracted", String(t.entities), "Entities the results yielded (one found by several results counts for each)"),
    stat("Facts extracted", String(t.edges)),
    stat("Facts closed", String(t.invalidated_edges), "Stored facts that the news contradicted and so closed"),
  ];
  if (t.latency.count) {
    out.push(
      stat("Latency avg", fmtMs(t.latency.avg_ms)), stat("Latency median", fmtMs(t.latency.median_ms)),
      stat("Latency max", fmtMs(t.latency.max_ms)),
    );
  }
  out.push(stat("Cost", fmtCost(t.cost_usd), "Tokens and search fees of the searches, not of extracting their results"));
  return out;
}

//---------------------------------------------------------------------------------------------
// The modal

function sourcesList(sources: WebSource[]): HTMLElement {
  const ul = h("ul", "ws-sources");
  for (const s of sources) {
    const li = h("li");
    li.append(link(s.url, s.title || s.url));
    if (s.title) li.append(" ", h("code", undefined, s.url));
    if (s.page_age) li.append(h("span", "muted", ` · ${s.page_age}`));
    ul.append(li);
  }
  return ul;
}

function entityChips(entities: WebEntity[]): HTMLElement {
  const box = h("div", "ws-chips");
  for (const e of entities) {
    const chip = h("span", e.only_web ? "chip chip-web" : "chip", e.name);
    chip.append(h("span", "muted", ` ${e.type}`));
    const tip = [e.description, e.only_web ? "Only known from web searches: no text sent by the user mentions it." : ""].filter(Boolean);
    if (tip.length) chip.title = tip.join("\n");
    box.append(chip);
  }
  return box;
}

function factsList(edges: WebEdge[]): HTMLElement {
  const ul = h("ul", "ws-facts");
  for (const e of edges) {
    const li = h("li");
    li.append(h("strong", undefined, e.source), h("span", "muted", ` —${e.relation}→ `), h("strong", undefined, e.target));
    if (e.description) li.append(h("span", undefined, `: ${e.description}`));
    if (e.created === false) li.append(" ", h("span", "badge badge-unknown", "restated"));
    ul.append(li);
  }
  return ul;
}

function statusBadge(w: WebSearch): HTMLElement {
  if (w.error) return h("span", "badge badge-error", "failed");
  return w.found ? h("span", "badge badge-ok", "found") : h("span", "badge badge-unknown", "nothing found");
}

function searchItem(w: WebSearch, n: number): HTMLElement {
  const item = h("details", "ws-item");
  const sum = item.appendChild(h("summary"));
  sum.append(h("strong", undefined, w.entity ? `${w.entity}` : `Search ${n}`));
  if (w.entity_type) sum.append(h("span", "muted", ` (${w.entity_type})`));
  sum.append(" ", statusBadge(w));
  const bits: string[] = [];
  if (w.latency_ms !== undefined) bits.push(fmtMs(w.latency_ms));
  if (w.sources?.length) bits.push(plural(w.sources.length, "source"));
  if (w.entities?.length) bits.push(plural(w.entities.length, "entity", "entities"));
  if (w.edges?.length) bits.push(plural(w.edges.length, "fact"));
  if (w.cost_usd) bits.push(fmtCost(w.cost_usd));
  sum.append(h("span", "muted", bits.length ? ` · ${bits.join(" · ")}` : ""));

  const body = item.appendChild(h("div", "ws-body"));
  const props = h("dl", "props");
  const row = (k: string, v: string | undefined) => { if (v) props.append(h("dt", undefined, k), h("dd", undefined, v)); };
  row("query issued", w.query);
  row("started", fmtTime(w.started_at));
  row("latency", w.latency_ms !== undefined ? fmtMs(w.latency_ms) : undefined);
  row("model", w.model);
  row("tokens in / out", w.prompt_tokens || w.completion_tokens ? `${w.prompt_tokens ?? 0} / ${w.completion_tokens ?? 0}` : undefined);
  row("cost", w.cost_usd ? fmtCost(w.cost_usd) : undefined);
  row("search-tool searches", w.billed_searches ? String(w.billed_searches) : undefined);
  row("result artifact", w.artifact_id ? `#${w.artifact_id}` : undefined);
  row("searched entity came from", w.parent_artifact_id ? `artifact #${w.parent_artifact_id}${w.parent_text ? `: ${w.parent_text}` : ""}` : undefined);
  row("trajectory", w.trajectory_id);
  body.append(props);
  if (w.error) body.append(h("p", "error", w.error));
  if (w.search_errors?.length) body.append(h("p", "error", `Search errors: ${w.search_errors.join(", ")}`));

  if (w.searches?.length) {
    body.append(h("h4", undefined, `Queries the model sent to the search engine (${w.searches.length})`));
    const ul = h("ul", "ws-queries");
    for (const q of w.searches) ul.append(h("li", undefined, q));
    body.append(ul);
  }
  if (w.briefing) body.append(h("h4", undefined, "Result"), h("pre", undefined, w.briefing));
  else if (!w.error) body.append(h("p", "muted", "The search found nothing relevant, so nothing was stored."));
  if (w.sources?.length) body.append(h("h4", undefined, w.found ? `Sources (${w.sources.length})` : `Pages the search returned (${w.sources.length})`), sourcesList(w.sources));
  if (w.entities?.length) {
    body.append(h("h4", undefined, `Entities from this result (${w.entities.length})`), entityChips(w.entities));
  }
  if (w.edges?.length) body.append(h("h4", undefined, `Facts from this result (${w.edges.length})`), factsList(w.edges));
  if (w.invalidated_edges?.length) {
    body.append(h("p", "muted", `Closed stored facts (contradicted by this result): ${w.invalidated_edges.map((i) => `#${i}`).join(", ")}`));
  }
  return item;
}

// Opens the modal titled title with the given web searches. summary (a line of totals) goes in the header; note, if any,
// explains what the list lacks.
export function showWebSearches(title: string, searches: WebSearch[], summary: string, note?: string): void {
  const modal = el<HTMLDialogElement>("websearch-modal");
  el("websearch-title").textContent = title;
  const status = el("websearch-status");
  status.className = "muted";
  status.textContent = summary;
  const content = el("websearch-content");
  content.replaceChildren();
  if (note) content.append(h("p", "muted", note));
  if (searches.length === 0) content.append(h("p", "muted", "No web searches."));
  searches.forEach((w, i) => content.append(searchItem(w, i + 1)));
  // A single search is shown open.
  if (searches.length === 1) (content.querySelector("details") as HTMLDetailsElement).open = true;
  if (!modal.open) modal.showModal();
}

// Opens the modal with the web searches stored in a dataset (read from its web_search artifacts).
export async function showDatasetWebSearches(db: string, datasetID: number, title: string): Promise<void> {
  try {
    const data = await getJSON<{ summary: DatasetSummary; searches: WebSearch[] }>(
      `api/web-searches/dataset?${new URLSearchParams({ db, dataset_id: String(datasetID) })}`);
    const s = data.summary;
    showWebSearches(
      `Web searches: ${title}`, data.searches,
      `${plural(s.artifacts, "result")} · ${plural(s.sources, "source")} · ${plural(s.entities, "entity", "entities")} (${s.only_web} only from the web) · ${plural(s.edges, "new fact")}`,
      "Read from the database: only searches that found something are stored, and their latency, cost and the queries sent to the search engine are not. " +
        "Those are in the trajectory of an eval.",
    );
  } catch (err) {
    void alertDialog(`Failed to load web searches: ${errText(err)}`);
  }
}

//---------------------------------------------------------------------------------------------
// The dashboard card

function datasetsTable(db: string, rows: DatasetRow[]): HTMLElement {
  const t = h("table", "compact");
  const head = t.createTHead().insertRow();
  for (const [label, num] of [["User", false], ["Dataset", false], ["Results", true], ["Entities", true], ["Only web", true], ["New facts", true], ["", false]] as [string, boolean][]) {
    head.append(h("th", num ? "num" : undefined, label));
  }
  const body = t.createTBody();
  for (const d of rows) {
    const tr = body.insertRow();
    tr.insertCell().textContent = d.user_id;
    tr.insertCell().textContent = d.deleted ? `${d.name} (deleted)` : d.name;
    for (const n of [d.artifacts, d.entities, d.only_web, d.edges]) {
      const td = tr.insertCell();
      td.className = "num";
      td.textContent = String(n);
    }
    const btn = h("button", "small", "View");
    btn.type = "button";
    btn.addEventListener("click", () => void showDatasetWebSearches(db, d.id, `${d.user_id} / ${d.name}`));
    tr.insertCell().append(btn);
  }
  return t;
}

function databaseBlock(db: string, s: Stats): HTMLElement {
  const box = h("div", "ws-db");
  box.append(h("h3", undefined, `${db} database`));
  const d = s.database;
  if (d.artifacts === 0 && !s.trajectories?.entity_searches) {
    box.append(h("p", "muted", "No web searches."));
    return box;
  }
  box.append(h("h4", undefined, "Stored in the database"));
  const dl = h("dl", "stats ws-stats");
  dl.append(
    stat("Web search artifacts", String(d.artifacts), "Searches that found something: each result is an artifact of kind web_search"),
    stat("Datasets", String(d.datasets)),
    stat("Searched from", String(d.parents), "Distinct artifacts whose entities were searched for"),
    stat("Sources", String(d.sources), "Pages cited by the results"),
    stat("Entities from searches", String(d.entities), "Distinct entities linked to a web search result"),
    stat("Only from the web", String(d.only_web), "Of those, entities that no text sent by the user mentions"),
    stat("New facts", String(d.edges), "Facts created by a result"),
    stat("Facts supported", String(d.supported_edges), "Facts created or restated by a result"),
  );
  box.append(dl);
  if (s.datasets.length) box.append(datasetsTable(db, s.datasets));
  if (s.trajectories && s.trajectories.entity_searches > 0) {
    box.append(h("h4", undefined, `Recorded in eval trajectories (all runs, ${plural(s.trajectories.calls, "remember call")})`));
    const tl = h("dl", "stats ws-stats");
    tl.append(...trajectoryStats(s.trajectories));
    box.append(tl);
  }
  return box;
}

let databases: string[] | undefined;

async function load(): Promise<void> {
  const status = el("websearch-card-status");
  const content = el("websearch-card-content");
  const includeDeleted = (document.getElementById("include-deleted") as HTMLInputElement | null)?.checked ?? false;
  status.className = "muted";
  status.textContent = "Loading…";
  try {
    if (!databases) databases = ((await getJSON<{ databases: string[] }>("api/databases")).databases) ?? [];
    const blocks: HTMLElement[] = [];
    for (const db of databases) {
      const params = new URLSearchParams({ db });
      if (includeDeleted) params.set("include_deleted", "true");
      blocks.push(databaseBlock(db, await getJSON<Stats>(`api/web-searches?${params}`)));
    }
    content.replaceChildren(...blocks);
    status.textContent = includeDeleted ? "Includes deleted datasets." : "Deleted datasets are left out (tick “include deleted datasets” above).";
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load web search statistics: ${errText(err)}`;
  }
}

export function initWebSearch(): void {
  const modal = el<HTMLDialogElement>("websearch-modal");
  el("websearch-close").addEventListener("click", () => modal.close());
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
  el("websearch-refresh").addEventListener("click", () => void load());
  document.getElementById("include-deleted")?.addEventListener("change", () => void load());
  void load();
}
