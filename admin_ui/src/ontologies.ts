// The "ontologies" view: a force-directed d3 graph (d3 is a global, loaded by index.html) of an ontology. The nodes are its classes, laid
// out by depth in the hierarchy; solid arrows go from a class to its parents (subClassOf), dashed arrows are the relations (from each
// class of the domain to each class of the range). Clicking a class shows its description, aliases, properties and relations.

import { onViewShown } from "./views.js";

declare const d3: typeof import("d3");

type Names = string | string[] | null | undefined;
interface Prop { type: string; description?: string; enum?: string[]; pattern?: string; required?: boolean }
interface ClassDef { subClassOf?: Names; description?: string; properties?: Record<string, Prop>; aliases?: string[] }
interface RelDef { description?: string; domain?: Names; range?: Names; temporal?: boolean; properties?: Record<string, Prop>; aliases?: string[] }
interface Spec { classes: Record<string, ClassDef>; relations?: Record<string, RelDef> }
interface Ontology {
  id: number; user_id: string; name: string; description?: string; version: number; classes: number; relations: number; spec?: Spec;
}

interface ClassNode extends d3.SimulationNodeDatum {
  name: string; def: ClassDef; parents: string[]; depth: number; root: string; degree: number;
}
interface ClassLink extends d3.SimulationLinkDatum<ClassNode> { kind: "sub" | "rel"; relation: string; def?: RelDef }

const asList = (n: Names): string[] => (n == null ? [] : typeof n === "string" ? [n] : n);

// Names are compared ignoring case and anything that is not a letter or a digit (as the server does).
const key = (s: string): string => s.toLowerCase().replace(/[^\p{L}\p{N}]/gu, "");

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

// The classes and links of a spec. Names that the spec does not define (it is validated by the server, so there should be none) are skipped.
export function buildModel(spec: Spec): { nodes: ClassNode[]; links: ClassLink[] } {
  const defs = Object.entries(spec.classes ?? {});
  const byKey = new Map(defs.map(([name]) => [key(name), name]));
  const resolve = (n: string): string | undefined => byKey.get(key(n));
  const parentsOf = (name: string): string[] =>
    asList(spec.classes[name]?.subClassOf).map(resolve).filter((p): p is string => !!p);

  const depthOf = (name: string, seen: readonly string[] = []): number =>
    seen.includes(name) ? 0 : parentsOf(name).reduce((d, p) => Math.max(d, depthOf(p, [...seen, name]) + 1), 0);
  const rootOf = (name: string, seen: readonly string[] = []): string => {
    const p = parentsOf(name)[0];
    return p && !seen.includes(p) ? rootOf(p, [...seen, name]) : name;
  };

  const subLinks: ClassLink[] = defs.flatMap(([name]) =>
    parentsOf(name).map((p): ClassLink => ({ source: name, target: p, kind: "sub", relation: "subClassOf" })));
  const relLinks: ClassLink[] = Object.entries(spec.relations ?? {}).flatMap(([rel, def]) =>
    asList(def.domain).map(resolve).filter((d): d is string => !!d).flatMap((d) =>
      asList(def.range).map(resolve).filter((r): r is string => !!r).map((r): ClassLink => ({ source: d, target: r, kind: "rel", relation: rel, def }))));
  const links = [...subLinks, ...relLinks];

  const degree = (name: string): number => links.filter((l) => l.source === name || l.target === name).length;
  const nodes: ClassNode[] = defs.map(([name, def]) => ({
    name, def, parents: parentsOf(name), depth: depthOf(name), root: rootOf(name), degree: degree(name),
  }));
  return { nodes, links };
}

// Properties of a class: those of its ancestors first, and its own (which may repeat an inherited one) last.
function allProps(spec: Spec, name: string, seen: readonly string[] = []): [string, Prop][] {
  const byKey = new Map(Object.keys(spec.classes).map((n) => [key(n), n]));
  const def = spec.classes[name];
  if (!def || seen.includes(name)) return [];
  const inherited = asList(def.subClassOf).map((p) => byKey.get(key(p))).filter((p): p is string => !!p).flatMap((p) => allProps(spec, p, [...seen, name]));
  return [...new Map([...inherited, ...Object.entries(def.properties ?? {})]).entries()];
}

const propText = ([n, p]: [string, Prop]): string =>
  `${n}: ${p.type}${p.enum ? ` {${p.enum.join(", ")}}` : ""}${p.pattern ? ` /${p.pattern}/` : ""}${p.required ? " (required)" : ""}`;

let stop: (() => void) | undefined;
let ontologies: Ontology[] = [];

function describe(spec: Spec, n: ClassNode, links: ClassLink[]): HTMLElement[] {
  const h = node("h3", "", n.name);
  const dl = node("dl");
  for (const [k, v] of [["parents", n.parents.join(", ")], ["description", n.def.description ?? ""], ["aliases", (n.def.aliases ?? []).join(", ")]]) {
    if (!v) continue;
    dl.append(node("dt", "", k), node("dd", "", v));
  }
  const out: HTMLElement[] = [h, dl];
  const props = allProps(spec, n.name);
  if (props.length) {
    out.push(node("h4", "", "Properties"), ...props.map((p) => node("div", "", propText(p))));
  }
  const name = (x: unknown): string => (x as ClassNode).name;
  const rels = links.filter((l) => l.kind === "rel" && (name(l.source) === n.name || name(l.target) === n.name));
  if (rels.length) {
    out.push(node("h4", "", `Relations (${rels.length})`), ...rels.map((l) => {
      const o = name(l.source) === n.name;
      const t = node("div", "", o ? `${l.relation} → ${name(l.target)}` : `${name(l.source)} → ${l.relation}`);
      if (l.def?.temporal) t.append(node("span", "graph-span", "temporal"));
      if (l.def?.description) t.title = l.def.description;
      return t;
    }));
  }
  return out;
}

function draw(container: HTMLElement, o: Ontology): void {
  stop?.();
  container.replaceChildren();
  const spec = o.spec;
  if (!spec || Object.keys(spec.classes ?? {}).length === 0) {
    container.append(node("p", "muted", "This ontology has no classes."));
    return;
  }
  const width = container.clientWidth || 1000;
  const height = container.clientHeight || 650;
  const { nodes, links } = buildModel(spec);

  const roots = [...new Set(nodes.map((n) => n.root))].sort();
  const color = d3.scaleOrdinal<string, string>([...d3.schemeTableau10, ...d3.schemeSet3]).domain(roots);
  const radius = (n: ClassNode): number => 5 + Math.min(9, Math.sqrt(n.degree) * 1.6);

  const svg = d3.create("svg").attr("viewBox", [0, 0, width, height]).attr("width", "100%").attr("height", "100%");
  const defs = svg.append("defs");
  for (const [id, cls] of [["onto-arrow-sub", "onto-arrow-sub"], ["onto-arrow-rel", "onto-arrow-rel"]]) {
    defs.append("marker").attr("id", id).attr("viewBox", "0 -4 8 8").attr("refX", 8).attr("refY", 0)
      .attr("markerWidth", 6).attr("markerHeight", 6).attr("orient", "auto")
      .append("path").attr("d", "M0,-4L8,0L0,4Z").attr("class", cls);
  }
  const root = svg.append("g");
  const zoom = d3.zoom<SVGSVGElement, undefined>().scaleExtent([0.2, 6]).on("zoom", (ev) => root.attr("transform", ev.transform));
  svg.call(zoom);

  const link = root.append("g").selectAll<SVGLineElement, ClassLink>("line").data(links).join("line")
    .attr("class", (l) => `onto-link onto-${l.kind}`)
    .attr("marker-end", (l) => `url(#onto-arrow-${l.kind})`);
  link.append("title").text((l) => (l.kind === "sub" ? "subClassOf" : `${l.relation}${l.def?.description ? `: ${l.def.description}` : ""}`));
  const linkLabel = root.append("g").selectAll<SVGTextElement, ClassLink>("text").data(links.filter((l) => l.kind === "rel")).join("text")
    .attr("class", "graph-label graph-edge-label").attr("text-anchor", "middle").text((l) => l.relation);

  const nodeSel = root.append("g").selectAll<SVGGElement, ClassNode>("g").data(nodes).join("g").attr("class", "graph-node");
  nodeSel.append("circle").attr("r", radius).attr("fill", (n) => color(n.root));
  nodeSel.append("text").text((n) => n.name).attr("class", "graph-label").attr("dy", (n) => radius(n) + 7).attr("text-anchor", "middle");
  nodeSel.append("title").text((n) => `${n.name}${n.def.description ? `\n${n.def.description}` : ""}`);

  const overlay = node("div", "graph-overlay onto-overlay");
  overlay.hidden = true;
  const showRels = el<HTMLInputElement>("onto-show-relations");
  const showNames = el<HTMLInputElement>("onto-show-names");

  // Applies the toggles and the selected class: the neighbours of the selection stay, the rest fades.
  let selected: ClassNode | undefined;
  const nameOf = (x: unknown): string => (x as ClassNode).name;
  const apply = (): void => {
    const near = new Set(selected ? [selected.name, ...links.filter((l) => nameOf(l.source) === selected?.name || nameOf(l.target) === selected?.name)
      .flatMap((l) => [nameOf(l.source), nameOf(l.target)])] : []);
    const touches = (l: ClassLink): boolean => !selected || nameOf(l.source) === selected.name || nameOf(l.target) === selected.name;
    link.style("display", (l) => (l.kind === "rel" && !showRels.checked ? "none" : null)).style("opacity", (l) => (touches(l) ? null : 0.06));
    linkLabel.style("display", (l) => (showRels.checked && (showNames.checked || (selected && touches(l))) ? null : "none"));
    nodeSel.style("opacity", (n) => (!selected || near.has(n.name) ? null : 0.2)).classed("selected", (n) => n === selected);
  };
  const select = (n: ClassNode | undefined): void => {
    selected = n;
    overlay.hidden = !n;
    if (n) overlay.replaceChildren(...describe(spec, n, links));
    apply();
  };
  nodeSel.on("click", (ev, n) => { ev.stopPropagation(); select(n === selected ? undefined : n); });
  svg.on("click", () => select(undefined));
  showRels.onchange = apply;
  showNames.onchange = apply;
  apply();

  const sim = d3.forceSimulation(nodes)
    .force("link", d3.forceLink<ClassNode, ClassLink>(links).id((n) => n.name)
      .distance((l) => (l.kind === "sub" ? 40 : 160)).strength((l) => (l.kind === "sub" ? 0.9 : 0.02)))
    .force("charge", d3.forceManyBody().strength(-140))
    .force("x", d3.forceX(width / 2).strength(0.07))
    .force("y", d3.forceY<ClassNode>((n) => n.depth * 90).strength(0.35))
    .force("collide", d3.forceCollide<ClassNode>().radius((n) => radius(n) + 12));
  const render = (): void => {
    const at = (x: unknown): ClassNode => x as ClassNode;
      // Arrows stop at the rim of the target circle.
      const rim = (l: ClassLink): [number, number] => {
        const s = at(l.source), t = at(l.target);
        const dx = (t.x ?? 0) - (s.x ?? 0), dy = (t.y ?? 0) - (s.y ?? 0);
        const len = Math.hypot(dx, dy) || 1;
        const r = radius(t) + 1;
        return [(t.x ?? 0) - (dx / len) * r, (t.y ?? 0) - (dy / len) * r];
      };
      link.attr("x1", (l) => at(l.source).x ?? 0).attr("y1", (l) => at(l.source).y ?? 0)
        .attr("x2", (l) => rim(l)[0]).attr("y2", (l) => rim(l)[1]);
      linkLabel.attr("x", (l) => ((at(l.source).x ?? 0) + (at(l.target).x ?? 0)) / 2)
        .attr("y", (l) => ((at(l.source).y ?? 0) + (at(l.target).y ?? 0)) / 2);
      nodeSel.attr("transform", (n) => `translate(${n.x ?? 0},${n.y ?? 0})`);
  };
  sim.on("tick", render);
  // Settle the layout before the first draw, then zoom to fit it (the user can still pan and zoom).
  sim.stop();
  sim.tick(300);
  render();
  const xs = nodes.map((n) => n.x ?? 0), ys = nodes.map((n) => n.y ?? 0);
  const [x0, x1, y0, y1] = [Math.min(...xs) - 30, Math.max(...xs) + 30, Math.min(...ys) - 30, Math.max(...ys) + 30];
  const k = Math.min(2, width / (x1 - x0), height / (y1 - y0));
  svg.call(zoom.transform, d3.zoomIdentity.translate((width - k * (x0 + x1)) / 2, (height - k * (y0 + y1)) / 2).scale(k));
  sim.alpha(0.05).restart();
  nodeSel.call(
    d3.drag<SVGGElement, ClassNode>()
      .on("start", (ev, n) => { if (!ev.active) sim.alphaTarget(0.3).restart(); n.fx = n.x; n.fy = n.y; })
      .on("drag", (ev, n) => { n.fx = ev.x; n.fy = ev.y; })
      .on("end", (ev, n) => { if (!ev.active) sim.alphaTarget(0); n.fx = null; n.fy = null; }),
  );

  // Legend: the root classes (the colour of a class is that of its first root ancestor) and the two kinds of arrow.
  const legend = node("div", "graph-legend onto-legend");
  for (const r of roots) {
    const item = legend.appendChild(node("span"));
    const dot = item.appendChild(node("i"));
    dot.style.background = color(r);
    item.append(`${r} (${nodes.filter((n) => n.root === r).length})`);
  }
  legend.append(node("span", "onto-key", "solid arrow: subClassOf"), node("span", "onto-key", "dashed arrow: relation"));

  container.append(svg.node() as SVGSVGElement, legend, overlay);
  stop = () => sim.stop();
}

const label = (o: Ontology): string => `${o.user_id} / ${o.name} (v${o.version})`;

async function load(): Promise<void> {
  const status = el("onto-status");
  const select = el<HTMLSelectElement>("onto-select");
  status.className = "muted";
  status.textContent = "Loading…";
  try {
    const res = await fetch("api/ontologies");
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    ontologies = ((await res.json()) as { ontologies: Ontology[] }).ontologies ?? [];
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load ontologies: ${err instanceof Error ? err.message : String(err)}`;
    return;
  }
  const previous = select.value;
  select.replaceChildren(...ontologies.map((o, i) => Object.assign(node("option", "", label(o)), { value: String(i) })));
  if (ontologies.length === 0) {
    status.textContent = "No ontologies stored. Load one with POST /v1/gb/ontologies.";
    stop?.();
    el("onto-canvas").replaceChildren();
    return;
  }
  select.value = previous !== "" && ontologies[Number(previous)] ? previous : "0";
  show();
}

function show(): void {
  const o = ontologies[Number(el<HTMLSelectElement>("onto-select").value)];
  if (!o) return;
  el("onto-status").className = "muted";
  el("onto-status").textContent = `${o.classes} classes, ${o.relations} relations${o.description ? ` · ${o.description}` : ""}`;
  draw(el("onto-canvas"), o);
}

export function initOntologies(): void {
  el("onto-refresh").addEventListener("click", () => void load());
  el("onto-select").addEventListener("change", show);
  onViewShown("ontologies", () => void load());
  if (location.hash.slice(1) === "ontologies") void load(); // the view was shown (by the URL hash) before this hook existed
}
