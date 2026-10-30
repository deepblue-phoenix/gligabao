// The "Temporal changes" modal of a remember run (see docs/temporal.md#what-a-call-did-to-the-facts): what the call did to the
// temporal state of the facts, drawn as a small SVG plot, plus a table of the events. d3 is loaded as a global by index.html.
//
// The x-axis is event time (when the facts hold in the world). Each fact the run touched is a row above the axis: a dot at its
// valid_at with a SOLID line down to the axis (it is established there), a bar to its invalid_at, and a DOTTED line down to the
// axis at the invalid_at (it was invalidated there); a fact with no end has an arrow (it still stands). When the run changed the end
// of a fact, a faint dashed bar shows what was believed before. The vertical dashed lines at the top are the reference times of the
// texts (the artifacts' valid_at). System time (when we learned or superseded a fact) is in the tooltips and in the table.

declare const d3: typeof import("d3");

interface Edge {
  id?: number; source: string; target: string; relation: string; description?: string;
  valid_at?: string; invalid_at?: string; created_at?: string; expired_at?: string;
}
interface EdgeEvent {
  id: number; edge_id: number; artifact_id: number; event: string;
  invalid_at_before?: string; invalid_at_after?: string; caused_by_edge_id?: number; created_at: string; edge?: Edge;
}
interface Artifact { id: number; parent_id?: number; kind: string; valid_at: string; created_at: string }
// The response of a remember run (run_id, artifact_id) or of an eval case run (case_run_id, artifact_ids); only these two are used.
interface Response { artifacts: Artifact[]; events: EdgeEvent[] }

// Events, from the strongest (what the fact is drawn as) to the weakest.
const EVENTS = ["superseded", "invalidated", "ended", "created", "confirmed"] as const;
const EVENT_HELP: Record<string, string> = {
  created: "the call stored it as a new fact",
  confirmed: "the call stated a fact that was already stored; nothing changed",
  ended: "stored fact that the text said had ended: its invalid_at was set",
  invalidated: "stored fact that a new, contradicting fact closed",
  superseded: "new fact that was closed at once: it is about an earlier time than a stored fact it contradicts",
};

interface Fact {
  id: number;
  edge: Edge;
  events: EdgeEvent[];
  kind: string;               // the strongest event
  validAt: Date;
  end: Date | null;           // invalid_at at the end of the run; null: still true
  ghost: [Date, Date | null] | null; // what was believed before, when the run changed the end: from, to (null: open-ended)
}

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
const date = (s?: string | null): Date | null => {
  if (!s) return null;
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? null : d;
};
const sameTime = (a: Date | null, b: Date | null): boolean => (a === null || b === null ? a === b : a.getTime() === b.getTime());

// 2024-03-01 for a midnight, 2024-03-01 12:30:00 otherwise; "—" for none.
function fmt(d: Date | null | undefined, none = "—"): string {
  if (!d) return none;
  const s = d.toISOString().replace("T", " ").slice(0, 19);
  return s.endsWith(" 00:00:00") ? s.slice(0, 10) : s;
}

function factsOf(events: EdgeEvent[]): Fact[] {
  const byEdge = new Map<number, EdgeEvent[]>();
  for (const ev of events) {
    if (!ev.edge) continue;
    byEdge.set(ev.edge_id, [...(byEdge.get(ev.edge_id) ?? []), ev]);
  }
  const facts: Fact[] = [];
  for (const [id, evs] of byEdge) {
    const edge = evs[0].edge as Edge;
    const validAt = date(edge.valid_at);
    if (!validAt) continue;
    const kind = EVENTS.find((k) => evs.some((e) => e.event === k)) ?? "confirmed";
    const changing = evs.filter((e) => !sameTime(date(e.invalid_at_before), date(e.invalid_at_after)));
    const last = evs[evs.length - 1];
    const end = date(last.invalid_at_after);
    let ghost: Fact["ghost"] = null;
    if (changing.length > 0) {
      // The end believed before the first change, to the end now. Drawn from the one to the other.
      const from = date(changing[0].invalid_at_before);
      const lastChange = date(changing[changing.length - 1].invalid_at_after);
      if (lastChange && (!from || from.getTime() > lastChange.getTime())) ghost = [lastChange, from];
      else if (from && lastChange) ghost = [from, lastChange];
    }
    facts.push({ id, edge, events: evs, kind, validAt, end, ghost });
  }
  facts.sort((a, b) => a.validAt.getTime() - b.validAt.getTime() || a.id - b.id);
  return facts;
}

const factText = (e: Edge): string => `${e.source} —${e.relation}→ ${e.target}`;

function tooltip(f: Fact): string {
  const e = f.edge;
  const lines = [e.description || factText(e), `fact #${f.id}: ${f.events.map((x) => x.event).join(", ")}`,
    `valid_at (event time): ${fmt(f.validAt)}`,
    `invalid_at (event time): ${f.end ? fmt(f.end) : "— (still true)"}`,
    `created_at (system time): ${fmt(date(e.created_at))}`,
    `expired_at (system time): ${e.expired_at ? fmt(date(e.expired_at)) : "— (live)"}`];
  return lines.join("\n");
}

function plot(facts: Fact[], artifacts: Artifact[]): SVGSVGElement {
  const W = 920, left = 24, right = 36, top = 40, rowH = 54, axisH = 36;
  const refs = new Map<number, Artifact[]>();
  for (const a of artifacts) {
    const t = date(a.valid_at);
    if (t) refs.set(t.getTime(), [...(refs.get(t.getTime()) ?? []), a]);
  }

  const times: number[] = [...refs.keys()];
  for (const f of facts) {
    times.push(f.validAt.getTime());
    if (f.end) times.push(f.end.getTime());
    if (f.ghost) times.push(f.ghost[0].getTime(), ...(f.ghost[1] ? [f.ghost[1].getTime()] : []));
  }
  let lo = Math.min(...times), hi = Math.max(...times);
  if (hi === lo) { lo -= 15 * 864e5; hi += 15 * 864e5; }
  const pad = (hi - lo) * 0.08;
  const x = d3.scaleUtc().domain([new Date(lo - pad), new Date(hi + pad)]).range([left, W - right]);
  const xMax = W - right;

  const y0 = top + facts.length * rowH;           // the x-axis
  const H = y0 + axisH;
  const svg = d3.create("svg").attr("viewBox", [0, 0, W, H]).attr("width", "100%").attr("class", "tp-plot").attr("role", "img")
    .attr("aria-label", "Temporal changes of the facts");

  // Reference times of the texts.
  const refG = svg.append("g").attr("class", "tp-refs");
  for (const [t, arts] of refs) {
    const rx = x(new Date(t));
    const g = refG.append("g");
    g.append("line").attr("x1", rx).attr("x2", rx).attr("y1", top - 14).attr("y2", y0);
    g.append("text").attr("x", rx).attr("y", top - 20).attr("text-anchor", "middle").text(`reference time ${fmt(new Date(t))}`);
    g.append("title").text(arts.map((a) => `artifact ${a.id} (${a.kind}): reference time ${fmt(new Date(t))}`).join("\n"));
  }

  const axis = svg.append("g").attr("class", "tp-axis").attr("transform", `translate(0,${y0})`).call(d3.axisBottom(x).ticks(8));
  axis.append("text").attr("class", "tp-axis-label").attr("x", xMax).attr("y", 30).attr("text-anchor", "end").text("event time (valid_at / invalid_at) →");

  facts.forEach((f, i) => {
    const by = y0 - (i + 0.5) * rowH + 6;          // the bar of the row
    const sx = x(f.validAt);
    const g = svg.append("g").attr("class", `tp-fact tp-c-${f.kind}`);
    g.append("title").text(tooltip(f));

    // What was believed before the call changed the end of the fact.
    if (f.ghost) {
      const gx0 = x(f.ghost[0]), gx1 = f.ghost[1] ? x(f.ghost[1]) : xMax;
      g.append("line").attr("class", "tp-ghost").attr("x1", gx0).attr("x2", gx1).attr("y1", by).attr("y2", by);
      if (!f.ghost[1]) g.append("path").attr("class", "tp-ghost-arrow").attr("d", `M${xMax - 7},${by - 4}L${xMax},${by}L${xMax - 7},${by + 4}`);
    }

    // Established: a solid line down to the axis at valid_at, then the bar while it stands.
    const ex = f.end ? x(f.end) : xMax;
    g.append("line").attr("class", "tp-drop").attr("x1", sx).attr("x2", sx).attr("y1", by).attr("y2", y0);
    g.append("line").attr("class", "tp-bar").attr("x1", sx).attr("x2", Math.max(sx, ex)).attr("y1", by).attr("y2", by);
    g.append("circle").attr("class", "tp-start").attr("cx", sx).attr("cy", by).attr("r", 4.5);
    if (f.end) {
      // Invalidated: a dotted line down to the axis at invalid_at.
      g.append("line").attr("class", "tp-drop tp-dotted").attr("x1", ex).attr("x2", ex).attr("y1", by).attr("y2", y0);
      g.append("circle").attr("class", "tp-stop").attr("cx", ex).attr("cy", by).attr("r", 4.5);
    } else {
      g.append("path").attr("class", "tp-arrow").attr("d", `M${xMax - 8},${by - 5}L${xMax},${by}L${xMax - 8},${by + 5}Z`);
    }

    const late = sx > W * 0.6;
    g.append("text").attr("class", "tp-label").attr("x", late ? sx + 6 : sx - 6).attr("y", by - 11)
      .attr("text-anchor", late ? "end" : "start").text(factText(f.edge));
  });
  return svg.node() as SVGSVGElement;
}

function legend(): HTMLElement {
  const box = node("div", "tp-legend");
  for (const k of EVENTS) {
    const s = box.appendChild(node("span", `tp-key tp-c-${k}`));
    s.title = EVENT_HELP[k];
    s.append(node("i"), k);
  }
  box.appendChild(node("span", "muted", "solid drop line: established (valid_at) · dotted drop line: invalidated (invalid_at) · faint dashed bar: believed before the call"));
  return box;
}

function eventsTable(events: EdgeEvent[]): HTMLTableElement {
  const table = node("table", "compact tp-events");
  const head = table.createTHead().insertRow();
  for (const h of ["Fact", "", "Event", "invalid_at before → after", "Caused by", "System time"]) head.appendChild(node("th", "", h));
  const body = table.createTBody();
  for (const ev of events) {
    const tr = body.insertRow();
    tr.insertCell().textContent = `#${ev.edge_id}`;
    tr.insertCell().textContent = ev.edge ? (ev.edge.description || factText(ev.edge)) : "";
    const kind = tr.insertCell();
    const b = kind.appendChild(node("span", `tp-key tp-c-${ev.event}`));
    b.title = EVENT_HELP[ev.event] ?? "";
    b.append(node("i"), ev.event);
    tr.insertCell().textContent = `${fmt(date(ev.invalid_at_before), "open")} → ${fmt(date(ev.invalid_at_after), "open")}`;
    tr.insertCell().textContent = ev.caused_by_edge_id ? `#${ev.caused_by_edge_id}` : "";
    tr.insertCell().textContent = fmt(date(ev.created_at));
  }
  return table;
}

function show(box: HTMLElement, data: Response): void {
  box.replaceChildren();
  const facts = factsOf(data.events);
  if (facts.length === 0) {
    box.className = "modal-body muted";
    box.textContent = "No fact was created or changed (or it ran before the temporal events were recorded).";
    return;
  }
  box.className = "modal-body";
  const counts = EVENTS.map((k) => [k, data.events.filter((e) => e.event === k).length] as const).filter(([, n]) => n > 0);
  const at = data.events.map((e) => e.created_at).sort()[0];
  box.appendChild(node("p", "muted", `${counts.map(([k, n]) => `${n} ${k}`).join(", ")} · system time ${fmt(date(at))} UTC`));
  box.appendChild(legend());
  box.appendChild(plot(facts, data.artifacts));
  box.appendChild(eventsTable(data.events));
}

// Opens the modal with the temporal changes fetched from `url` (api/remember-run-temporal or api/evals/case_runs/{id}/temporal),
// under `title`; fetched each time it is opened.
export function openTemporalAt(title: string, url: string): void {
  const modal = el<HTMLDialogElement>("tp-modal");
  const box = el("tp-body");
  el("tp-title").textContent = title;
  box.className = "modal-body muted";
  box.textContent = "Loading…";
  if (!modal.open) modal.showModal();
  fetch(url).then(async (res) => {
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    const data = (await res.json()) as Response;
    if (modal.open && el("tp-title").textContent === title) show(box, data);
  }).catch((err: unknown) => {
    box.className = "modal-body error";
    box.textContent = `Failed to load the temporal changes: ${errText(err)}`;
  });
}

// Opens the modal on what run `runId` of database `db` did to the facts.
export function openTemporal(db: string, runId: number): void {
  openTemporalAt(`Run ${runId} · temporal changes`, `api/remember-run-temporal?${new URLSearchParams({ db, run_id: String(runId) })}`);
}

export function initTemporal(): void {
  const modal = el<HTMLDialogElement>("tp-modal");
  el("tp-close").addEventListener("click", () => modal.close());
}
