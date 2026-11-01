// The "Temporal changes" modal of a remember run (see docs/temporal.md#what-a-call-did-to-the-facts): what the call did to the
// temporal state of the facts, drawn as a small SVG plot, plus a table of the events. d3 is loaded as a global by index.html.
//
// The x-axis is event time (when the facts hold in the world). Each fact the run touched is a row above the axis: a dot at its
// valid_at with a SOLID line down to the axis (it is established there), a bar to its invalid_at, and a DOTTED line down to the
// axis at the invalid_at (it was invalidated there); a fact with no end has an arrow (it still stands). When the run changed the end
// of a fact, a faint dashed bar shows what was believed before. The vertical dashed lines at the top are the reference times of the
// texts (the artifacts' valid_at). System time (when we learned or superseded a fact) is in the tooltips and in the table.
//
// Opened for an eval case, the plot also has a CHECKS LAYER (toggled with the button at its side): for each fact the case expects
// (expect_edges), a lane under the fact's row holds what the case wanted (valid_at ... invalid_at, or open) next to what was stored,
// with a diamond for every check that held (teal, tick) or failed (magenta, cross) and a bold magenta stretch for the gap between
// the expected and the stored time. A fact the case expects that was never stored gets a row of its own. The "Expand" button under
// the plot widens it and makes it taller.

declare const d3: typeof import("d3");
type G = import("d3").Selection<SVGGElement, undefined, null, undefined>;

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

// What an eval case expects of a fact (core.KnowledgeExpectEdge) and its checks (GET api/evals/case_runs/{id}/debug).
interface ExpectEdge { source: string; target: string; valid_at?: string; invalid_at?: string; ended?: boolean; superseded?: boolean }
interface CaseChecks { problems?: string[]; passed_checks?: string[]; expected?: { expect_edges?: ExpectEdge[] } }
type Status = "held" | "failed" | "none";
// One expected fact with the outcome of each of its checks, and the stored fact it was checked against (null: none was stored).
interface Expect {
  want: ExpectEdge; desc: string; fact: Fact | null;
  from: Date | null;          // where the expected span starts (the stored fact's valid_at if the case does not say)
  to: Date | null;            // where it ends; null with want.ended: ended at an unknown time; null without: still true
  stored: Status; valid: Status; invalid: Status; ended: Status; superseded: Status;
  lines: { ok: boolean; text: string }[];
}

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

const combine = (...ss: Status[]): Status => (ss.includes("failed") ? "failed" : ss.includes("held") ? "held" : "none");
const sameName = (a: string, b: string): boolean => a.toLowerCase() === b.toLowerCase();

// The expected facts of a case, each with its checks read from the check texts the case run recorded ("Source -> Target: ...", see
// evals/knowledge/case.go) and the stored fact the case compared it with (the first one between the same entities, like the case does).
function expectsOf(facts: Fact[], checks: CaseChecks | undefined): Expect[] {
  const out: Expect[] = [];
  for (const want of checks?.expected?.expect_edges ?? []) {
    const desc = `${want.source} -> ${want.target}`;
    const fact = facts.filter((f) => sameName(f.edge.source, want.source) && sameName(f.edge.target, want.target)).sort((a, b) => a.id - b.id)[0] ?? null;
    const lines = [
      ...(checks?.passed_checks ?? []).filter((c) => c.startsWith(`${desc}: `)).map((text) => ({ ok: true, text })),
      ...(checks?.problems ?? []).filter((c) => c.startsWith(`${desc}: `)).map((text) => ({ ok: false, text })),
    ];
    const status = (what: string): Status => {
      const l = lines.filter((x) => x.text.startsWith(`${desc}: ${what}`));
      return l.some((x) => !x.ok) ? "failed" : l.length > 0 ? "held" : "none";
    };
    const to = date(want.invalid_at) ?? (want.ended ? fact?.end ?? null : null);
    out.push({
      want, desc, fact, from: date(want.valid_at) ?? fact?.validAt ?? null, to,
      stored: lines.some((x) => x.text.startsWith(`${desc}: no edge stored`)) ? "failed" : status("edge stored"),
      valid: status("valid_at"), invalid: status("invalid_at"), ended: status("ended"), superseded: status("superseded"), lines,
    });
  }
  return out;
}

const overall = (e: Expect): Status => combine(e.stored, e.valid, e.invalid, e.ended, e.superseded);
const days = (a: Date, b: Date): string => {
  const d = Math.abs(a.getTime() - b.getTime()) / 864e5;
  return d >= 1 ? `${Math.round(d * 10) / 10}d` : `${Math.round(d * 24 * 10) / 10}h`;
};

interface PlotOptions { checks?: Expect[]; expanded?: boolean; showChecks?: boolean }
interface Row { fact: Fact | null; exps: Expect[]; ty: number; h: number }

function plot(facts: Fact[], artifacts: Artifact[], opt: PlotOptions = {}): SVGSVGElement {
  const expanded = opt.expanded ?? false;
  const exps = opt.checks ?? [];
  const W = expanded ? 1900 : 920, left = 24, right = 36, top = 40, axisH = 36;
  const rowBase = expanded ? 80 : 54, laneH = expanded ? 26 : 22;
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
  for (const e of exps) {
    if (e.from) times.push(e.from.getTime());
    if (e.to) times.push(e.to.getTime());
  }
  if (times.length === 0) times.push(Date.now());
  let lo = Math.min(...times), hi = Math.max(...times);
  if (hi === lo) { lo -= 15 * 864e5; hi += 15 * 864e5; }
  const pad = (hi - lo) * 0.08;
  const x = d3.scaleUtc().domain([new Date(lo - pad), new Date(hi + pad)]).range([left, W - right]);
  const xMax = W - right;

  // Rows from the axis up: the facts (earliest at the bottom), then a row for each expected fact that was not stored. A row has a
  // lane under its bar for each expected fact that was checked against it.
  const rows: Row[] = facts.map((f) => ({ fact: f, exps: exps.filter((e) => e.fact === f), ty: 0, h: 0 }));
  for (const e of exps) if (!e.fact) rows.push({ fact: null, exps: [e], ty: 0, h: 0 });
  let used = 0;
  for (const r of rows) {
    r.h = r.fact ? rowBase + r.exps.length * laneH : 36 + laneH;
    used += r.h;
  }
  const y0 = top + used;                          // the x-axis
  const H = y0 + axisH;
  let yb = y0;
  for (const r of rows) { r.ty = yb - r.h; yb = r.ty; }

  const svg = d3.create("svg").attr("viewBox", [0, 0, W, H]).attr("width", expanded ? W : "100%").attr("class", "tp-plot").attr("role", "img")
    .attr("aria-label", "Temporal changes of the facts");
  if (exps.length > 0 && opt.showChecks === false) svg.classed("tp-hide-ck", true);

  // Reference times of the texts.
  const refG = svg.append("g").attr("class", "tp-refs");
  for (const [t, arts] of refs) {
    const rx = x(new Date(t));
    const g = refG.append("g");
    g.append("line").attr("x1", rx).attr("x2", rx).attr("y1", top - 14).attr("y2", y0);
    g.append("text").attr("x", rx).attr("y", top - 20).attr("text-anchor", "middle").text(`reference time ${fmt(new Date(t))}`);
    g.append("title").text(arts.map((a) => `artifact ${a.id} (${a.kind}): reference time ${fmt(new Date(t))}`).join("\n"));
  }

  const axis = svg.append("g").attr("class", "tp-axis").attr("transform", `translate(0,${y0})`).call(d3.axisBottom(x).ticks(expanded ? 14 : 8));
  axis.append("text").attr("class", "tp-axis-label").attr("x", xMax).attr("y", 30).attr("text-anchor", "end").text("event time (valid_at / invalid_at) →");

  const barY = (r: Row): number => r.ty + rowBase - 21;   // the bar of a fact row
  // The checks layer: under the facts, so that it never covers a dot or a line of them.
  if (exps.length > 0) {
    const layer = svg.append("g").attr("class", "tp-checks");
    for (const r of rows) {
      const by = barY(r);
      r.exps.forEach((e, i) => drawExpect(layer, e, r.fact ? by + 13 + i * laneH : r.ty + 26, r.fact ? by : null));
    }
  }

  rows.forEach((r) => {
    const f = r.fact;
    if (!f) return;
    const by = barY(r);
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

  // One diamond of a check: teal with a tick when it held, magenta with a cross when it failed, hollow when the case did not check it.
  function marker(g: G, cx: number, cy: number, st: Status): void {
    const m = g.append("g").attr("class", `tp-ck-mark tp-ck-${st}`).attr("transform", `translate(${cx},${cy})`);
    m.append("path").attr("class", "tp-ck-diamond").attr("d", "M0,-7L7,0L0,7L-7,0Z");
    if (st === "held") m.append("path").attr("class", "tp-ck-glyph").attr("d", "M-3,0.5L-1,2.8L3,-2.5");
    if (st === "failed") m.append("path").attr("class", "tp-ck-glyph").attr("d", "M-2.6,-2.6L2.6,2.6M2.6,-2.6L-2.6,2.6");
  }

  // The lane of one expected fact at height ly: its expected span (a pale track), the gaps to the stored fact (bold magenta, with a
  // dashed tick up to the stored dot, at barAt), a diamond for each check, and what held and failed in words under it.
  function drawExpect(layer: G, e: Expect, ly: number, barAt: number | null): void {
    const ov = overall(e);
    const g = layer.append("g").attr("class", `tp-ck-lane tp-ck-${ov}`);
    g.append("title").text([`expected: ${e.desc}`,
      `valid_at ${e.want.valid_at ? fmt(date(e.want.valid_at)) : "(not checked)"} · invalid_at ${e.want.invalid_at ? fmt(date(e.want.invalid_at)) : "(not checked)"} · ended ${e.want.ended ? "yes" : "no"}${e.want.superseded ? " · superseded" : ""}`,
      e.fact ? `stored: fact #${e.fact.id}, valid_at ${fmt(e.fact.validAt)}, invalid_at ${e.fact.end ? fmt(e.fact.end) : "—"}` : "stored: no such fact",
      ...(e.lines.length ? ["", ...e.lines.map((l) => `${l.ok ? "✓ held" : "✗ failed"}: ${l.text}`)] : ["", "(no check of this fact in the case run)"])].join("\n"));
    g.append("rect").attr("class", "tp-ck-band").attr("x", left).attr("width", xMax - left).attr("y", ly - 9).attr("height", laneH - 2);

    const xs = e.from ? x(e.from) : left + 10;
    const open = !e.to && !e.want.ended;               // expected to be still true
    const xe = e.to ? x(e.to) : xMax;
    g.append("line").attr("class", "tp-ck-track").attr("x1", xs).attr("x2", Math.max(xs, xe)).attr("y1", ly).attr("y2", ly);

    const gaps: string[] = [];
    const gap = (a: number, b: number, tickAt: number | null): void => {
      g.append("line").attr("class", "tp-ck-gap").attr("x1", a).attr("x2", b).attr("y1", ly).attr("y2", ly);
      if (barAt !== null && tickAt !== null) g.append("line").attr("class", "tp-ck-tick").attr("x1", tickAt).attr("x2", tickAt).attr("y1", barAt + 6).attr("y2", ly);
    };
    if (e.fact && e.want.valid_at && e.from && !sameTime(e.from, e.fact.validAt)) {
      gap(x(e.from), x(e.fact.validAt), x(e.fact.validAt));
      gaps.push(`start Δ ${days(e.from, e.fact.validAt)}`);
    }
    if (e.fact && (e.to || open) && !sameTime(e.to, e.fact.end)) {
      gap(xe, e.fact.end ? x(e.fact.end) : xMax, e.fact.end ? x(e.fact.end) : null);
      gaps.push(e.to && e.fact.end ? `end Δ ${days(e.to, e.fact.end)}` : open ? "ended, expected open" : "still open, expected end");
    }

    if (e.want.valid_at && e.from) marker(g, xs, ly, e.valid);
    const endSt = combine(e.invalid, e.ended);
    if (e.to) marker(g, xe, ly, endSt);
    else if (e.want.ended) marker(g, xMax - 8, ly, endSt === "none" ? "failed" : endSt); // ended at a time the case does not say, not stored as such
    else g.append("path").attr("class", `tp-ck-open tp-ck-${endSt}`).attr("d", `M${xMax - 9},${ly - 6}L${xMax - 1},${ly}L${xMax - 9},${ly + 6}`);

    const glyph = (st: Status): string => (st === "held" ? "✓" : "✗");
    const parts = ([["stored", e.stored], ["valid_at", e.valid], ["invalid_at", e.invalid], ["ended", e.ended], ["superseded", e.superseded]] as const)
      .filter(([, st]) => st !== "none").map(([n, st]) => `${glyph(st)} ${n}`);
    const late = xs > W * 0.5;
    g.append("text").attr("class", "tp-ck-text").attr("x", late ? xs + 7 : xs - 7).attr("y", ly + 10).attr("text-anchor", late ? "end" : "start")
      .text([`expected: ${parts.join("  ") || "not checked"}`, ...gaps].join("  ·  "));
    if (!e.fact) g.append("text").attr("class", "tp-ck-name").attr("x", late ? xs + 7 : xs - 7).attr("y", ly - 13).attr("text-anchor", late ? "end" : "start")
      .text(`${e.desc} (not stored)`);
  }
}

function legend(withChecks: boolean): HTMLElement {
  const box = node("div", "tp-legend");
  for (const k of EVENTS) {
    const s = box.appendChild(node("span", `tp-key tp-c-${k}`));
    s.title = EVENT_HELP[k];
    s.append(node("i"), k);
  }
  if (withChecks) {
    for (const [cls, text, help] of [["held", "check held", "an expected fact check that held"], ["failed", "check failed", "an expected fact check that failed"]] as const) {
      const s = box.appendChild(node("span", `tp-key tp-ck-key tp-ck-${cls}`));
      s.title = help;
      s.append(node("i"), text);
    }
    const gapKey = box.appendChild(node("span", "tp-key tp-ck-key tp-ck-failed"));
    gapKey.title = "the stretch of time between what the case expected and what was stored";
    gapKey.append(node("b"), "gap to expected");
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

const ICON_EYE = `<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M1 12s4-7 11-7 11 7 11 7-4 7-11 7S1 12 1 12z"/><circle cx="12" cy="12" r="3"/></svg>`;

// `checks` are the checks of the eval case the plot is opened for (undefined: not for a case, or they could not be read).
function show(box: HTMLElement, data: Response, checks?: CaseChecks): void {
  box.replaceChildren();
  const modal = el<HTMLDialogElement>("tp-modal");
  modal.classList.remove("tp-wide");
  const facts = factsOf(data.events);
  const exps = expectsOf(facts, checks);
  if (facts.length === 0 && exps.length === 0) {
    box.className = "modal-body muted";
    box.textContent = "No fact was created or changed (or it ran before the temporal events were recorded).";
    return;
  }
  box.className = "modal-body";
  const counts = EVENTS.map((k) => [k, data.events.filter((e) => e.event === k).length] as const).filter(([, n]) => n > 0);
  const at = data.events.map((e) => e.created_at).sort()[0];
  box.appendChild(node("p", "muted", facts.length === 0 ? "No fact was stored." : `${counts.map(([k, n]) => `${n} ${k}`).join(", ")} · system time ${fmt(date(at))} UTC`));
  box.appendChild(legend(exps.length > 0));

  let expanded = false, showChecks = true;
  const stage = box.appendChild(node("div", "tp-stage"));
  const plotBox = stage.appendChild(node("div", "tp-plotbox"));
  const draw = (): void => plotBox.replaceChildren(plot(facts, data.artifacts, { checks: exps, expanded, showChecks }));
  draw();

  if (exps.length > 0) {
    // The side button: shows or hides the whole checks layer (no redraw: the layer is hidden by a class of the plot).
    const side = stage.appendChild(node("div", "tp-side"));
    const toggle = side.appendChild(node("button", "small tp-layer-btn on"));
    toggle.type = "button";
    toggle.setAttribute("aria-pressed", "true");
    toggle.title = "Show or hide the layer of the checks the case expected (held and failed) over the facts";
    toggle.innerHTML = ICON_EYE;
    toggle.append(" Checks layer");
    toggle.addEventListener("click", () => {
      showChecks = !showChecks;
      toggle.classList.toggle("on", showChecks);
      toggle.setAttribute("aria-pressed", String(showChecks));
      plotBox.querySelector("svg")?.classList.toggle("tp-hide-ck", !showChecks);
    });
  }

  const bar = box.appendChild(node("div", "tp-bar-below"));
  const expand = bar.appendChild(node("button", "small", "Expand"));
  expand.type = "button";
  expand.title = "Make the plot wider and taller (it scrolls sideways when it is wider than the window)";
  expand.addEventListener("click", () => {
    expanded = !expanded;
    expand.textContent = expanded ? "Collapse" : "Expand";
    modal.classList.toggle("tp-wide", expanded);
    draw();
  });
  box.appendChild(eventsTable(data.events));
}

// The expected facts and the checks of the case run, to put over the plot; undefined when they cannot be read (the plot is then drawn without).
async function caseChecks(caseRunId: number): Promise<CaseChecks | undefined> {
  try {
    const res = await fetch(`api/evals/case_runs/${caseRunId}/debug`);
    return res.ok ? ((await res.json()) as CaseChecks) : undefined;
  } catch {
    return undefined;
  }
}

// Opens the modal with the temporal changes fetched from `url` (api/remember-run-temporal or api/evals/case_runs/{id}/temporal),
// under `title`; fetched each time it is opened. For an eval case, pass its `caseRunId`: the plot then also has the layer of the
// facts and checks the case expected, which held and which failed.
export function openTemporalAt(title: string, url: string, caseRunId?: number): void {
  const modal = el<HTMLDialogElement>("tp-modal");
  const box = el("tp-body");
  el("tp-title").textContent = title;
  box.className = "modal-body muted";
  box.textContent = "Loading…";
  if (!modal.open) modal.showModal();
  const checks = caseRunId === undefined ? Promise.resolve(undefined) : caseChecks(caseRunId);
  fetch(url).then(async (res) => {
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    const data = (await res.json()) as Response;
    const ck = await checks;
    if (modal.open && el("tp-title").textContent === title) show(box, data, ck);
  }).catch((err: unknown) => {
    box.className = "modal-body error";
    box.textContent = `Failed to load the temporal changes: ${errText(err)}`;
  });
}

// Opens the modal on the temporal changes of eval case run `caseRunId`, with the checks of the case over the plot.
export function openTemporalCase(title: string, caseRunId: number): void {
  openTemporalAt(title, `api/evals/case_runs/${caseRunId}/temporal`, caseRunId);
}

// Opens the modal on what run `runId` of database `db` did to the facts.
export function openTemporal(db: string, runId: number): void {
  openTemporalAt(`Run ${runId} · temporal changes`, `api/remember-run-temporal?${new URLSearchParams({ db, run_id: String(runId) })}`);
}

export function initTemporal(): void {
  const modal = el<HTMLDialogElement>("tp-modal");
  el("tp-close").addEventListener("click", () => modal.close());
  modal.addEventListener("close", () => modal.classList.remove("tp-wide"));
}
