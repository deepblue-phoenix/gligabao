// Process-flow view of an ATIF trajectory, shown in a modal. d3 is loaded as a global by index.html.
//
// The steps form a vertical spine (start → step 1 → step 2 → … → end) whose edges are labeled with the time between
// steps. A step that calls tools gets a tool node to its right (edge "calls") and the tool's result node right of
// that (edge "returns"). Clicking any node shows its full content in a side panel.

declare const d3: typeof import("d3");

interface ToolCall { tool_call_id: string; function_name: string; arguments?: Record<string, unknown> }
interface ObservationResult { source_call_id?: string; content: string }
interface Metrics { prompt_tokens: number; completion_tokens: number; cached_tokens?: number; cost_usd?: number }
interface Step {
  step_id: number; timestamp?: string; source: string; model_name?: string; message: string;
  reasoning_content?: string; tool_calls?: ToolCall[]; observation?: { results: ObservationResult[] };
  metrics?: Metrics; extra?: Record<string, unknown>;
}
interface Trajectory {
  trajectory_id: string; session_id?: string; schema_version?: string;
  agent: { name: string; version?: string; model_name?: string };
  steps: Step[]; notes?: string; extra?: Record<string, unknown>;
  final_metrics?: { total_prompt_tokens: number; total_completion_tokens: number; total_cost_usd: number; total_steps: number };
}

type Kind = "start" | "end" | "system" | "user" | "agent" | "tool" | "result";

interface FlowNode {
  id: string; kind: Kind; x: number; y: number; w: number; h: number;
  title: string; subtitle: string; error?: boolean;
  detail: () => HTMLElement[];
}
interface FlowEdge { from: FlowNode; to: FlowNode; label: string; kind: "next" | "call" | "return" }

const STEP_W = 250, STEP_H = 50, TOOL_W = 210, TOOL_H = 44, GAP_X = 90, ROW_GAP = 46, END_R = 14;

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

let stop: (() => void) | undefined;

function clear(): void {
  stop?.();
  stop = undefined;
  el("trace-canvas").replaceChildren();
  el("trace-detail").replaceChildren();
}

const COLORS: Record<Kind, string> = {
  start: "#8b92a3", end: "#8b92a3", system: "#b392f0", user: "#6ea8fe", agent: "#3fb950", tool: "#f0a868", result: "#e3c05f",
};

// Opens the modal and shows the trajectory (fetched from the admin API). title labels the modal (e.g. the eval).
export async function showTrace(trajectoryID: string, title: string): Promise<void> {
  const modal = el<HTMLDialogElement>("trace-modal");
  const status = el("trace-status");
  el("trace-title").textContent = title;
  status.className = "muted";
  status.textContent = "Loading…";
  clear();
  if (!modal.open) modal.showModal();
  try {
    const res = await fetch(`api/evals/trajectories/${encodeURIComponent(trajectoryID)}`);
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    const traj = (await res.json()) as Trajectory;
    if (!modal.open) return; // closed while loading
    const fm = traj.final_metrics;
    status.textContent = [
      traj.agent.name,
      `${traj.steps.length} step${traj.steps.length === 1 ? "" : "s"}`,
      fm ? `${fm.total_prompt_tokens}→${fm.total_completion_tokens} tokens` : "",
      fm ? `$${fm.total_cost_usd.toFixed(4)}` : "",
    ].filter(Boolean).join(" · ");
    draw(el("trace-canvas"), traj);
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load trace: ${err instanceof Error ? err.message : String(err)}`;
  }
}

function oneLine(s: string, max: number): string {
  const line = s.replace(/\s+/g, " ").trim();
  return line.length > max ? `${line.slice(0, max - 1)}…` : line;
}

function fmtDelta(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "";
  if (ms < 1000) return `+${Math.round(ms)} ms`;
  if (ms < 60000) return `+${(ms / 1000).toFixed(1)} s`;
  return `+${Math.floor(ms / 60000)} min ${Math.round((ms % 60000) / 1000)} s`;
}

function pre(text: string): HTMLElement {
  const p = document.createElement("pre");
  p.textContent = text; // textContent only: never inject server data as HTML
  return p;
}

function heading(text: string): HTMLElement {
  const h = document.createElement("h4");
  h.textContent = text;
  return h;
}

function props(rows: [string, string | undefined][]): HTMLElement {
  const dl = document.createElement("dl");
  for (const [k, v] of rows) {
    if (!v) continue;
    dl.appendChild(document.createElement("dt")).textContent = k;
    dl.appendChild(document.createElement("dd")).textContent = v;
  }
  return dl;
}

const json = (v: unknown): string => JSON.stringify(v, null, 2);

function stepDetail(s: Step): HTMLElement[] {
  const m = s.metrics;
  const out: HTMLElement[] = [
    props([
      ["step", String(s.step_id)], ["source", s.source], ["time", s.timestamp], ["model", s.model_name],
      ["tokens", m ? `${m.prompt_tokens} in${m.cached_tokens ? ` (${m.cached_tokens} cached)` : ""}, ${m.completion_tokens} out` : undefined],
      ["cost", m?.cost_usd !== undefined ? `$${m.cost_usd.toFixed(5)}` : undefined],
    ]),
    heading("Message"), pre(s.message || "(empty)"),
  ];
  if (s.reasoning_content) out.push(heading("Reasoning"), pre(s.reasoning_content));
  if (s.extra && Object.keys(s.extra).length) out.push(heading("Extra"), pre(json(s.extra)));
  return out;
}

// Lays the trajectory out and returns the nodes and edges of the flow.
function layout(traj: Trajectory): { nodes: FlowNode[]; edges: FlowEdge[] } {
  const nodes: FlowNode[] = [];
  const edges: FlowEdge[] = [];
  const spineX = 0, toolX = STEP_W + GAP_X, resultX = toolX + TOOL_W + GAP_X;
  const failed = !!traj.notes && /^failed/i.test(traj.notes);

  const start: FlowNode = {
    id: "start", kind: "start", x: spineX + STEP_W / 2 - END_R, y: 0, w: END_R * 2, h: END_R * 2, title: "start", subtitle: "",
    detail: () => {
      const input = traj.extra?.input;
      return [
        props([
          ["trajectory", traj.trajectory_id], ["session", traj.session_id], ["schema", traj.schema_version],
          ["agent", `${traj.agent.name}${traj.agent.version ? ` ${traj.agent.version}` : ""}`], ["model", traj.agent.model_name],
        ]),
        ...(input !== undefined ? [heading("Input"), pre(json(input))] : []),
      ];
    },
  };
  nodes.push(start);

  let y = END_R * 2 + ROW_GAP;
  let prev: FlowNode = start;
  let prevTime: number | undefined;
  for (const s of traj.steps) {
    const calls = s.tool_calls ?? [];
    const rowH = Math.max(STEP_H, calls.length * (TOOL_H + 12) - 12);
    const kind: Kind = s.source === "system" || s.source === "user" ? s.source : "agent";
    const m = s.metrics;
    const node: FlowNode = {
      id: `step-${s.step_id}`, kind, x: spineX, y: y + (rowH - STEP_H) / 2, w: STEP_W, h: STEP_H,
      title: `#${s.step_id} ${s.source}${s.model_name ? ` · ${s.model_name}` : ""}`,
      subtitle: oneLine(s.message, m ? 24 : 38) || (calls.length ? "(tool call)" : "(empty)"),
      detail: () => stepDetail(s),
    };
    nodes.push(node);
    const t = s.timestamp ? new Date(s.timestamp).getTime() : undefined;
    const delta = t !== undefined && prevTime !== undefined ? fmtDelta(t - prevTime) : "";
    edges.push({ from: prev, to: node, label: delta, kind: "next" });
    if (t !== undefined) prevTime = t;
    if (m) node.subtitle += ` · ${m.prompt_tokens}→${m.completion_tokens} tok`;

    const results = s.observation?.results ?? [];
    const used = new Set<ObservationResult>();
    calls.forEach((c, i) => {
      const cy = y + i * (TOOL_H + 12);
      const args = c.arguments ?? {};
      const tool: FlowNode = {
        id: `tool-${s.step_id}-${i}`, kind: "tool", x: toolX, y: cy, w: TOOL_W, h: TOOL_H,
        title: c.function_name, subtitle: oneLine(Object.keys(args).length ? json(args) : "(no arguments)", 30),
        detail: () => [props([["step", String(s.step_id)], ["call id", c.tool_call_id]]), heading("Function"), pre(c.function_name), heading("Arguments"), pre(json(args))],
      };
      nodes.push(tool);
      edges.push({ from: node, to: tool, label: "calls", kind: "call" });
      const res = results.find((r) => r.source_call_id === c.tool_call_id && !used.has(r)) ??
        (results.length === calls.length && !results[i].source_call_id ? results[i] : undefined);
      if (res) {
        used.add(res);
        const rn: FlowNode = {
          id: `result-${s.step_id}-${i}`, kind: "result", x: resultX, y: cy, w: TOOL_W, h: TOOL_H,
          title: `result of ${c.function_name}`, subtitle: oneLine(res.content, 30) || "(empty)",
          detail: () => [props([["step", String(s.step_id)], ["call id", res.source_call_id]]), heading("Content"), pre(res.content || "(empty)")],
        };
        nodes.push(rn);
        edges.push({ from: tool, to: rn, label: "returns", kind: "return" });
      }
    });
    // Observations that no tool call claimed hang off the step itself.
    results.filter((r) => !used.has(r)).forEach((r, i) => {
      const rn: FlowNode = {
        id: `obs-${s.step_id}-${i}`, kind: "result", x: toolX, y: y + (calls.length + i) * (TOOL_H + 12), w: TOOL_W, h: TOOL_H,
        title: "observation", subtitle: oneLine(r.content, 30) || "(empty)",
        detail: () => [props([["step", String(s.step_id)], ["call id", r.source_call_id]]), heading("Content"), pre(r.content || "(empty)")],
      };
      nodes.push(rn);
      edges.push({ from: node, to: rn, label: "observes", kind: "call" });
    });
    y += Math.max(rowH, STEP_H + (results.length > calls.length ? (results.length - calls.length) * (TOOL_H + 12) : 0)) + ROW_GAP;
    prev = node;
  }

  const fm = traj.final_metrics;
  const end: FlowNode = {
    id: "end", kind: "end", x: spineX + STEP_W / 2 - END_R, y, w: END_R * 2, h: END_R * 2, title: failed ? "failed" : "end", subtitle: "", error: failed,
    detail: () => [
      props([
        ["steps", fm ? String(fm.total_steps) : undefined],
        ["tokens", fm ? `${fm.total_prompt_tokens} in, ${fm.total_completion_tokens} out` : undefined],
        ["cost", fm ? `$${fm.total_cost_usd.toFixed(5)}` : undefined],
      ]),
      ...(traj.notes ? [heading("Notes"), pre(traj.notes)] : []),
    ],
  };
  nodes.push(end);
  edges.push({ from: prev, to: end, label: "", kind: "next" });
  return { nodes, edges };
}

function draw(container: HTMLElement, traj: Trajectory): void {
  const { nodes, edges } = layout(traj);
  const width = container.clientWidth || 900;
  const height = container.clientHeight || 600;

  const svg = d3.create("svg").attr("viewBox", [0, 0, width, height]).attr("width", "100%").attr("height", "100%");
  svg.append("defs").append("marker")
    .attr("id", "trace-arrow").attr("viewBox", "0 0 10 10").attr("refX", 9).attr("refY", 5)
    .attr("markerWidth", 7).attr("markerHeight", 7).attr("orient", "auto-start-reverse")
    .append("path").attr("d", "M0,0L10,5L0,10z").attr("class", "trace-arrow-head");
  const root = svg.append("g");
  const zoom = d3.zoom<SVGSVGElement, undefined>().scaleExtent([0.15, 3]).on("zoom", (ev) => root.attr("transform", ev.transform));
  svg.call(zoom);

  // Edges: the spine runs straight down, call/return edges run sideways.
  const edgePath = (e: FlowEdge): string => {
    if (e.kind === "next") {
      const x = e.from.x + e.from.w / 2;
      return `M${x},${e.from.y + e.from.h}L${e.to.x + e.to.w / 2},${e.to.y}`;
    }
    const x0 = e.from.x + e.from.w, y0 = e.from.y + e.from.h / 2, x1 = e.to.x, y1 = e.to.y + e.to.h / 2;
    const mx = (x0 + x1) / 2;
    return `M${x0},${y0}C${mx},${y0} ${mx},${y1} ${x1},${y1}`;
  };
  const edgeMid = (e: FlowEdge): [number, number] => e.kind === "next"
    ? [e.from.x + e.from.w / 2 + 6, (e.from.y + e.from.h + e.to.y) / 2]
    : [(e.from.x + e.from.w + e.to.x) / 2, (e.from.y + e.to.y) / 2 + e.from.h / 2 - 5];

  root.append("g").selectAll("path").data(edges).join("path")
    .attr("class", (e) => `trace-edge trace-edge-${e.kind}`).attr("d", edgePath).attr("marker-end", "url(#trace-arrow)");
  root.append("g").selectAll("text").data(edges.filter((e) => e.label)).join("text")
    .attr("class", (e) => `trace-edge-label trace-edge-label-${e.kind}`)
    .attr("text-anchor", (e) => (e.kind === "next" ? "start" : "middle"))
    .attr("transform", (e) => `translate(${edgeMid(e)})`).text((e) => e.label);

  const node = root.append("g").selectAll<SVGGElement, FlowNode>("g").data(nodes).join("g")
    .attr("class", (n) => `trace-node trace-node-${n.kind}`).attr("transform", (n) => `translate(${n.x},${n.y})`);
  node.append("title").text((n) => `${n.title}${n.subtitle ? `\n${n.subtitle}` : ""}`);
  node.filter((n) => n.kind === "start" || n.kind === "end").append("circle")
    .attr("cx", END_R).attr("cy", END_R).attr("r", END_R).attr("fill", (n) => (n.error ? "#f85149" : COLORS[n.kind]));
  node.filter((n) => n.kind === "start" || n.kind === "end").append("text")
    .attr("class", "trace-end-label").attr("x", END_R * 2 + 8).attr("y", END_R + 4).text((n) => n.title);
  const boxes = node.filter((n) => n.kind !== "start" && n.kind !== "end");
  boxes.append("rect").attr("width", (n) => n.w).attr("height", (n) => n.h)
    .attr("rx", (n) => (n.kind === "tool" ? n.h / 2 : 6)).attr("stroke", (n) => COLORS[n.kind]);
  boxes.append("text").attr("class", "trace-title").attr("x", 10).attr("y", 19).text((n) => oneLine(n.title, 34));
  boxes.append("text").attr("class", "trace-subtitle").attr("x", 10).attr("y", 36).text((n) => n.subtitle);

  // Side panel with the full content of the selected node.
  const panel = el("trace-detail");
  const select = (n: FlowNode | undefined): void => {
    node.classed("selected", (d) => d === n);
    if (!n) { panel.replaceChildren(); return; }
    const h = document.createElement("h3");
    h.textContent = n.title;
    panel.replaceChildren(h, ...n.detail());
    panel.scrollTop = 0;
  };
  node.on("click", (ev, n) => { ev.stopPropagation(); select(n); });
  svg.on("click", () => select(undefined));
  select(nodes[0]);

  // Legend of node kinds present in the flow.
  const legend = document.createElement("div");
  legend.className = "graph-legend";
  const kinds = new Set(nodes.map((n) => n.kind));
  const names: [Kind, string][] = [["system", "system prompt"], ["user", "user message"], ["agent", "agent step"], ["tool", "tool call"], ["result", "tool result"]];
  for (const [k, label] of names) {
    if (!kinds.has(k)) continue;
    const item = legend.appendChild(document.createElement("span"));
    const dot = item.appendChild(document.createElement("i"));
    dot.style.background = COLORS[k];
    item.append(label);
  }

  container.append(svg.node() as SVGSVGElement, legend);

  // Fit the flow to the canvas width, starting at the top (long traces are then scrolled/panned downwards).
  const bounds = { x0: Math.min(...nodes.map((n) => n.x)), x1: Math.max(...nodes.map((n) => n.x + n.w + (n.kind === "start" || n.kind === "end" ? 60 : 0))) };
  const flowW = bounds.x1 - bounds.x0;
  const k = Math.min(1, Math.max(0.3, (width - 40) / flowW));
  svg.call(zoom.transform, d3.zoomIdentity.translate((width - flowW * k) / 2 - bounds.x0 * k, 20).scale(k));
  stop = () => svg.on(".zoom", null);
}

export function initTraceModal(): void {
  const modal = el<HTMLDialogElement>("trace-modal");
  el("trace-close").addEventListener("click", () => modal.close());
  modal.addEventListener("close", clear);
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
}
