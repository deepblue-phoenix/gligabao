// Force-directed d3 view of a dataset's entity graph, shown in a modal. d3 is loaded as a global by index.html.

declare const d3: typeof import("d3");

// model_id is the ID of the model (see GraphData.models) that inferred the entity / fact (absent if unknown).
interface Entity { id: number; name: string; type: string; description?: string; model_id?: string }
interface Edge {
  id: number; source_id: number; target_id: number; relation: string; description?: string; model_id?: string;
  // Bi-temporal validity of the fact: event time (valid_at/invalid_at) and system time (created_at/expired_at).
  valid_at?: string; invalid_at?: string; created_at?: string; expired_at?: string;
}
export interface Model { id: string; name: string; provider?: string }
export interface GraphData { dataset: string; entities: Entity[]; edges: Edge[]; models?: Model[] }

// Name of the model with the given ID (the ID itself if the graph does not list it).
let modelNames = new Map<string, string>();
function modelName(id: string | undefined): string {
  return id ? (modelNames.get(id) ?? id) : "";
}

interface Node extends d3.SimulationNodeDatum, Entity { degree: number }
interface Link extends d3.SimulationLinkDatum<Node> { edge: Edge; slot: number }

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

let stop: (() => void) | undefined;

function clear(): void {
  stop?.();
  stop = undefined;
  el("graph-canvas").replaceChildren();
}

async function fetchGraph(url: string): Promise<GraphData> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
  return (await res.json()) as GraphData;
}

// Opens the modal and shows the graph of a live dataset (fetched from the admin API).
export function showGraph(db: string, userID: string, dataset: string): Promise<void> {
  const params = new URLSearchParams({ db, user_id: userID, dataset });
  return showGraphFrom(`${userID} / ${dataset}`, () => fetchGraph(`api/graph?${params}`));
}

// Opens the modal and shows the graph stored at url (an admin API endpoint answering with a GraphData JSON), e.g. the
// graph an eval built. This and showGraph are the entry points; any other place that shows a graph should use them.
export function showGraphAt(title: string, url: string): Promise<void> {
  return showGraphFrom(title, () => fetchGraph(url));
}

// Opens the modal titled title and draws the graph that load returns.
export async function showGraphFrom(title: string, load: () => Promise<GraphData>): Promise<void> {
  const modal = el<HTMLDialogElement>("graph-modal");
  const status = el("graph-status");
  el("graph-title").textContent = title;
  status.className = "muted";
  status.textContent = "Loading…";
  clear();
  if (!modal.open) modal.showModal();

  try {
    const data = await load();
    if (!modal.open) return; // closed while loading
    if (data.entities.length === 0) {
      status.textContent = "The graph is empty.";
      return;
    }
    modelNames = new Map((data.models ?? []).map((m) => [m.id, m.name]));
    const models = [...new Set([...data.entities, ...data.edges].map((x) => modelName(x.model_id)).filter((m) => m))].sort();
    status.textContent = `${data.entities.length} entities, ${data.edges.length} edges` +
      (models.length ? ` · inferred by ${models.join(", ")}` : "");
    draw(el("graph-canvas"), data);
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load graph: ${err instanceof Error ? err.message : String(err)}`;
  }
}

function draw(container: HTMLElement, data: GraphData): void {
  const width = container.clientWidth || 900;
  const height = container.clientHeight || 600;

  const nodes: Node[] = data.entities.map((e) => ({ ...e, degree: 0 }));
  const byID = new Map(nodes.map((n) => [n.id, n]));
  const links: Link[] = [];
  const parallel = new Map<string, number>(); // edges seen so far per node pair, to spread their labels apart
  for (const edge of data.edges) {
    const s = byID.get(edge.source_id);
    const t = byID.get(edge.target_id);
    if (!s || !t) continue;
    s.degree++;
    t.degree++;
    const key = `${Math.min(s.id, t.id)}:${Math.max(s.id, t.id)}`;
    const slot = parallel.get(key) ?? 0;
    parallel.set(key, slot + 1);
    links.push({ source: s, target: t, edge, slot });
  }

  const types = [...new Set(nodes.map((n) => n.type))].sort();
  const color = d3.scaleOrdinal<string, string>(d3.schemeTableau10).domain(types);
  const radius = (n: Node): number => 4 + Math.min(10, Math.sqrt(n.degree) * 2);

  const svg = d3.create("svg").attr("viewBox", [0, 0, width, height]).attr("width", "100%").attr("height", "100%");
  const root = svg.append("g");
  svg.call(
    d3.zoom<SVGSVGElement, undefined>().scaleExtent([0.1, 8]).on("zoom", (ev) => { root.attr("transform", ev.transform); placeOverlay(); }),
  );

  const link = root.append("g").attr("class", "graph-links").selectAll("line").data(links).join("line")
    .attr("class", (l) => (l.edge.expired_at ? "graph-link expired" : "graph-link"));
  link.append("title").text((l) => `${l.edge.relation}${l.edge.description ? `: ${l.edge.description}` : ""}${l.edge.model_id ? `\nmodel: ${modelName(l.edge.model_id)}` : ""}`);

  const linkLabel = root.append("g").selectAll("text").data(links).join("text")
    .attr("class", "graph-label graph-edge-label").attr("text-anchor", "middle").text((l) => l.edge.relation);

  const node = root.append("g").selectAll<SVGGElement, Node>("g").data(nodes).join("g").attr("class", "graph-node");
  node.append("circle").attr("r", radius).attr("fill", (n) => color(n.type));
  node.append("text").text((n) => n.name).attr("class", "graph-label").attr("dy", (n) => radius(n) + 7).attr("text-anchor", "middle");
  node.append("title").text((n) => `${n.name} (${n.type})${n.description ? `\n${n.description}` : ""}${n.model_id ? `\nmodel: ${modelName(n.model_id)}` : ""}`);

  // Details overlay of the clicked node; it follows the node while the layout moves or the view is zoomed.
  const overlay = document.createElement("div");
  overlay.className = "graph-overlay";
  overlay.hidden = true;
  let selected: Node | undefined;

  const placeOverlay = (): void => {
    if (!selected) return;
    const ctm = root.node()?.getScreenCTM();
    if (!ctm) return;
    const box = container.getBoundingClientRect();
    const x = ctm.a * (selected.x ?? 0) + ctm.c * (selected.y ?? 0) + ctm.e - box.left;
    const y = ctm.b * (selected.x ?? 0) + ctm.d * (selected.y ?? 0) + ctm.f - box.top;
    const gap = radius(selected) * ctm.a + 8;
    // Put it right of the node, or left of it if there is no room; keep it inside the canvas vertically.
    const left = x + gap + overlay.offsetWidth > box.width ? x - gap - overlay.offsetWidth : x + gap;
    const top = Math.max(4, Math.min(y - 12, box.height - overlay.offsetHeight - 4));
    overlay.style.left = `${Math.max(4, left)}px`;
    overlay.style.top = `${top}px`;
  };
  const select = (n: Node | undefined): void => {
    selected = n;
    node.classed("selected", (d) => d === n);
    overlay.hidden = !n;
    if (n) {
      overlay.replaceChildren(...describe(n, data.edges, byID));
      placeOverlay();
    }
  };
  node.on("click", (ev, n) => { ev.stopPropagation(); select(n); });
  // A click on the background (not a node; d3 suppresses the click that ends a drag or pan) removes the overlay.
  svg.on("click", () => select(undefined));

  const sim = d3.forceSimulation(nodes)
    .force("link", d3.forceLink<Node, Link>(links).id((n) => n.id).distance(60))
    .force("charge", d3.forceManyBody().strength(-120))
    .force("center", d3.forceCenter(width / 2, height / 2))
    .force("collide", d3.forceCollide<Node>().radius((n) => radius(n) + 2))
    .on("tick", () => {
      link
        .attr("x1", (l) => (l.source as Node).x ?? 0).attr("y1", (l) => (l.source as Node).y ?? 0)
        .attr("x2", (l) => (l.target as Node).x ?? 0).attr("y2", (l) => (l.target as Node).y ?? 0);
      linkLabel.attr("transform", (l) => {
        const s = l.source as Node, t = l.target as Node;
        const dx = (t.x ?? 0) - (s.x ?? 0), dy = (t.y ?? 0) - (s.y ?? 0);
        const len = Math.hypot(dx, dy) || 1;
        const off = ((l.slot + 1) >> 1) * (l.slot % 2 ? 1 : -1) * 7; // 0, +7, -7, +14, ... perpendicular to the edge
        const x = ((s.x ?? 0) + (t.x ?? 0)) / 2 - (dy / len) * off;
        const y = ((s.y ?? 0) + (t.y ?? 0)) / 2 + (dx / len) * off;
        let angle = (Math.atan2(dy, dx) * 180) / Math.PI;
        if (angle > 90 || angle < -90) angle += 180; // keep text upright
        return `translate(${x},${y}) rotate(${angle})`;
      });
      node.attr("transform", (n) => `translate(${n.x ?? 0},${n.y ?? 0})`);
      placeOverlay();
    });

  node.call(
    d3.drag<SVGGElement, Node>()
      .on("start", (ev, n) => { if (!ev.active) sim.alphaTarget(0.3).restart(); n.fx = n.x; n.fy = n.y; })
      .on("drag", (ev, n) => { n.fx = ev.x; n.fy = ev.y; })
      .on("end", (ev, n) => { if (!ev.active) sim.alphaTarget(0); n.fx = null; n.fy = null; }),
  );

  // Legend of entity types.
  const legend = document.createElement("div");
  legend.className = "graph-legend";
  for (const t of types) {
    const item = legend.appendChild(document.createElement("span"));
    const dot = item.appendChild(document.createElement("i"));
    dot.style.background = color(t);
    item.append(t || "(no type)");
  }

  container.append(svg.node() as SVGSVGElement, legend, overlay);
  stop = () => sim.stop();
}

function fmtTime(t: string | undefined): string {
  if (!t) return "–";
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? t : d.toISOString().replace("T", " ").slice(0, 19) + " UTC";
}

// Human-readable length of the span between two timestamps in its largest fitting unit ("3 days", "1 month"), or
// undefined if either is missing or unparsable.
function fmtSpan(from: string | undefined, to: string | undefined): string | undefined {
  if (!from || !to) return undefined;
  const ms = new Date(to).getTime() - new Date(from).getTime();
  if (Number.isNaN(ms)) return undefined;
  const units: [string, number][] = [
    ["year", 365 * 864e5], ["month", 30 * 864e5], ["week", 7 * 864e5], ["day", 864e5],
    ["hour", 36e5], ["minute", 6e4], ["second", 1e3],
  ];
  const abs = Math.max(0, ms);
  for (const [name, size] of units) {
    if (abs >= size) {
      const n = Math.floor(abs / size);
      return `${n} ${name}${n === 1 ? "" : "s"}`;
    }
  }
  return "less than a second";
}

// Builds the overlay content: the entity's parameters, then the temporal data of the facts (edges) it takes part in.
// textContent only: never inject server data as HTML.
function describe(n: Node, edges: Edge[], byID: Map<number, Node>): HTMLElement[] {
  const h = document.createElement("h3");
  h.textContent = n.name;

  const params = document.createElement("dl");
  for (const [k, v] of [["id", String(n.id)], ["type", n.type], ["description", n.description ?? ""], ["model", modelName(n.model_id)], ["connections", String(n.degree)]]) {
    if (!v) continue;
    params.appendChild(document.createElement("dt")).textContent = k;
    params.appendChild(document.createElement("dd")).textContent = v;
  }

  const facts = document.createElement("div");
  const mine = edges.filter((e) => e.source_id === n.id || e.target_id === n.id);
  const title = facts.appendChild(document.createElement("h4"));
  title.textContent = `Temporal data (${mine.length} fact${mine.length === 1 ? "" : "s"})`;
  for (const e of mine) {
    const out = e.source_id === n.id;
    const other = byID.get(out ? e.target_id : e.source_id)?.name ?? "?";
    const fact = facts.appendChild(document.createElement("div"));
    fact.className = e.expired_at ? "graph-fact expired" : "graph-fact";
    fact.appendChild(document.createElement("strong")).textContent = out ? `${e.relation} → ${other}` : `${other} → ${e.relation}`;
    // How long the fact held: from when it became true until it ended (invalid_at), else until it was superseded
    // (expired_at); a fact that is still true has held until now.
    const end = e.invalid_at ?? e.expired_at;
    const span = fmtSpan(e.valid_at ?? e.created_at, end ?? new Date().toISOString());
    if (span) {
      const tag = fact.appendChild(document.createElement("span"));
      tag.className = "graph-span";
      tag.textContent = `valid for ${span}${end ? "" : " so far"}`;
    }
    const dl = fact.appendChild(document.createElement("dl"));
    for (const [k, v] of [["valid at", e.valid_at], ["invalid at", e.invalid_at], ["created at", e.created_at], ["expired at", e.expired_at]]) {
      dl.appendChild(document.createElement("dt")).textContent = k as string;
      dl.appendChild(document.createElement("dd")).textContent = fmtTime(v);
    }
    if (e.model_id) {
      dl.appendChild(document.createElement("dt")).textContent = "model";
      dl.appendChild(document.createElement("dd")).textContent = modelName(e.model_id);
    }
  }
  return [h, params, facts];
}

export function initGraphModal(): void {
  const modal = el<HTMLDialogElement>("graph-modal");
  el("graph-close").addEventListener("click", () => modal.close());
  modal.addEventListener("close", clear);
  // Click on the backdrop (the dialog element itself) closes it.
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
}
