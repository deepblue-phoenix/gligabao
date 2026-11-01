// The in-context use of entities and edges: how many times each went into the context of an LLM call (the admin graph endpoints
// answer with it as `in_context`, see internal/api/admin/in_context.go). This file has the shared helpers (the counts by pipeline), the
// small d3 histograms of the counts and the modal that shows them, per entity and then per edge. The graph view (graph.ts) shows the
// same numbers over its nodes and edges. d3 is loaded as a global by index.html.

import type { GraphData } from "./graph.js";

declare const d3: typeof import("d3");

// Times an item was in a context, per pipeline: `query` answering a question, `remember` ingesting a text.
export interface Use { query: number; remember: number }
export interface InContext { entities: Record<string, Use>; edges: Record<string, Use> }
// Which pipelines' counts are shown: both added, or one of them.
export type Mode = "all" | "query" | "remember";

export const MODES: [Mode, string][] = [["all", "all pipelines"], ["query", "query only"], ["remember", "remember only"]];

export function countOf(use: Use | undefined, mode: Mode): number {
  if (!use) return 0;
  return mode === "query" ? use.query : mode === "remember" ? use.remember : use.query + use.remember;
}

// Fills a <select> with the modes.
export function fillModeSelect(select: HTMLSelectElement): void {
  for (const [value, text] of MODES) {
    const o = select.appendChild(document.createElement("option"));
    o.value = value;
    o.textContent = text;
  }
}

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

interface Bin { label: string; n: number; zero: boolean }

// Bins the counts: one bin per value while they are few (0..24), else ~20 equal ranges.
function bins(values: number[]): Bin[] {
  const max = d3.max(values) ?? 0;
  if (max <= 24) {
    const out: Bin[] = Array.from({ length: max + 1 }, (_, k) => ({ label: String(k), n: 0, zero: k === 0 }));
    for (const v of values) out[v].n++;
    return out;
  }
  return d3.bin().domain([0, max + 1]).thresholds(20)(values).map((b) => {
    const x0 = Math.ceil(b.x0 ?? 0);
    const x1 = Math.ceil(b.x1 ?? 0) - 1;
    return { label: x0 >= x1 ? String(x0) : `${x0}–${x1}`, n: b.length, zero: false };
  });
}

// Draws a histogram of values (how many times each item was in a context): x the number of times, y the number of items with it.
// The bar of the items that never were is dimmed. `what` is the plural of the item ("entities", "edges").
export function drawHistogram(container: HTMLElement, values: number[], what: string, color: string): void {
  const width = 420, height = 190, m = { top: 8, right: 10, bottom: 38, left: 38 };
  const data = bins(values);
  const x = d3.scaleBand<string>().domain(data.map((b) => b.label)).range([m.left, width - m.right]).padding(0.12);
  const y = d3.scaleLinear().domain([0, d3.max(data, (b) => b.n) || 1]).nice().range([height - m.bottom, m.top]);

  const svg = d3.create("svg").attr("class", "hist").attr("viewBox", [0, 0, width, height]).attr("width", "100%");
  svg.append("g").selectAll("rect").data(data).join("rect")
    .attr("x", (b) => x(b.label) ?? 0).attr("width", x.bandwidth())
    .attr("y", (b) => y(b.n)).attr("height", (b) => y(0) - y(b.n))
    .attr("fill", (b) => (b.zero ? "#6b7280" : color))
    .append("title").text((b) => `${b.n} ${what} ${b.zero ? "never in a context" : `in a context ${b.label} time${b.label === "1" ? "" : "s"}`}`);
  const every = Math.ceil(data.length / 10); // thin the x labels out when there are many bins
  svg.append("g").attr("transform", `translate(0,${height - m.bottom})`)
    .call(d3.axisBottom(x).tickValues(data.filter((_, i) => i % every === 0).map((b) => b.label)).tickSizeOuter(0));
  svg.append("g").attr("transform", `translate(${m.left},0)`)
    .call(d3.axisLeft(y).ticks(4).tickFormat(d3.format("d")).tickSizeOuter(0));
  svg.append("text").attr("class", "hist-axis").attr("x", (m.left + width - m.right) / 2).attr("y", height - 4).attr("text-anchor", "middle")
    .text("times in an LLM context");
  svg.append("text").attr("class", "hist-axis").attr("transform", `translate(10,${(m.top + height - m.bottom) / 2}) rotate(-90)`)
    .attr("text-anchor", "middle").text(what);
  container.append(svg.node() as SVGSVGElement);
}

interface Item { name: string; count: number; use: Use | undefined }

// One section of the modal: the histogram of the items' counts, a line with the totals and the most used items.
function section(parent: HTMLElement, title: string, what: string, color: string, items: Item[], mode: Mode): void {
  const box = parent.appendChild(document.createElement("section"));
  box.className = "ic-section";
  box.appendChild(document.createElement("h3")).textContent = title;
  const total = items.reduce((s, i) => s + i.count, 0);
  const never = items.filter((i) => i.count === 0).length;
  const p = box.appendChild(document.createElement("p"));
  p.className = "muted";
  p.textContent = items.length === 0
    ? `No ${what}.`
    : `${items.length} ${what}, ${total} uses in a context, ${never} never in one (${Math.round((100 * never) / items.length)}%).`;
  if (items.length === 0) return;
  drawHistogram(box, items.map((i) => i.count), what, color);

  const top = items.filter((i) => i.count > 0).sort((a, b) => b.count - a.count || a.name.localeCompare(b.name)).slice(0, 10);
  if (top.length === 0) return;
  box.appendChild(document.createElement("h4")).textContent = `Most used ${what}`;
  const table = box.appendChild(document.createElement("table"));
  table.className = "compact";
  const head = table.createTHead().insertRow();
  for (const [label, num] of [["", false], ["Total", true], ["Query", true], ["Remember", true]] as [string, boolean][]) {
    const th = head.appendChild(document.createElement("th"));
    th.textContent = label;
    if (num) th.className = "num";
  }
  const body = table.createTBody();
  for (const i of top) {
    const tr = body.insertRow();
    tr.insertCell().textContent = i.name; // textContent only: never inject server data as HTML
    for (const n of [i.count, i.use?.query ?? 0, i.use?.remember ?? 0]) {
      const td = tr.insertCell();
      td.className = "num";
      td.textContent = String(n);
    }
  }
  if (mode !== "all") table.title = `The first column counts the ${mode} pipeline only.`;
}

let current: GraphData | undefined;

function render(): void {
  const body = el("in-context-body");
  body.replaceChildren();
  if (!current) return;
  const mode = el<HTMLSelectElement>("in-context-mode").value as Mode;
  const use = current.in_context;
  const names = new Map(current.entities.map((e) => [e.id, e.name]));
  const entities: Item[] = current.entities.map((e) => {
    const u = use?.entities[String(e.id)];
    return { name: e.name, count: countOf(u, mode), use: u };
  });
  const edges: Item[] = current.edges.map((e) => {
    const u = use?.edges[String(e.id)];
    return { name: `${names.get(e.source_id) ?? "?"} —${e.relation}→ ${names.get(e.target_id) ?? "?"}`, count: countOf(u, mode), use: u };
  });
  section(body, "Entities", "entities", "#4e79a7", entities, mode);
  section(body, "Edges", "edges", "#f28e2b", edges, mode);
}

// Opens the modal titled title with the histograms of the in-context counts of data (a graph the admin API answered with).
export function showInContextOf(title: string, data: GraphData): void {
  const modal = el<HTMLDialogElement>("in-context-modal");
  el("in-context-title").textContent = title;
  const status = el("in-context-status");
  status.className = "muted";
  status.textContent = data.in_context
    ? "How many times each entity and edge went into the context of an LLM call (every use is logged: see gb_graph_in_ctx_counters)."
    : "This graph has no in-context data.";
  current = data;
  render();
  if (!modal.open) modal.showModal();
}

// Opens the modal for a live dataset (fetched from the admin API).
export async function showInContext(db: string, userID: string, dataset: string): Promise<void> {
  const modal = el<HTMLDialogElement>("in-context-modal");
  const status = el("in-context-status");
  el("in-context-title").textContent = `${userID} / ${dataset}`;
  status.className = "muted";
  status.textContent = "Loading…";
  el("in-context-body").replaceChildren();
  current = undefined;
  if (!modal.open) modal.showModal();
  try {
    const res = await fetch(`api/graph?${new URLSearchParams({ db, user_id: userID, dataset })}`);
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    if (!modal.open) return; // closed while loading
    showInContextOf(`${userID} / ${dataset}`, (await res.json()) as GraphData);
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load: ${err instanceof Error ? err.message : String(err)}`;
  }
}

export function initInContextModal(): void {
  const modal = el<HTMLDialogElement>("in-context-modal");
  const mode = el<HTMLSelectElement>("in-context-mode");
  fillModeSelect(mode);
  mode.addEventListener("change", render);
  el("in-context-close").addEventListener("click", () => modal.close());
  modal.addEventListener("close", () => { current = undefined; el("in-context-body").replaceChildren(); });
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
}
