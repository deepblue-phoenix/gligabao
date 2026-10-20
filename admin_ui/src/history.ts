// History of one case within an experiment (d3, in a modal), as three plots one under the other, one dot per run that
// executed the case and x = when the run started: its status (shows when a failing case started to pass), its latency,
// and the number of checks that passed and failed. d3 is loaded as a global by index.html. Talks to /admin/api/evals/experiments/{id}/history.

declare const d3: typeof import("d3");

interface Point {
  run_id: number; run_started_at: string; status: string; verdict?: string; score?: number;
  problems?: string[]; passed_checks?: string[]; error?: string; steps: number; started_at: string; finished_at: string;
}
interface History { eval: string; name: string; points: Point[] }
interface Dot extends Point { t: Date; seconds: number; outcome: string }

// What the plots show of a result is the model's own word (internal/evals_neo/core): the verdict (pass or fail) of a completed
// case run that has one, otherwise its status (errored, cancelled, running, ...).
export const outcomeOf = (p: { status: string; verdict?: string }): string => (p.status === "completed" && p.verdict ? p.verdict : p.status);

const LANES = ["pass", "fail", "errored"];
const COLORS: Record<string, string> = { pass: "#3fb950", fail: "#f85149", errored: "#d29922" };
const OTHER_COLOR = "#8b949e";

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

const fmt = (d: Date): string => d.toISOString().replace("T", " ").slice(0, 19);

// Summarises the progression: the current state and, if it passes now, since which run it does.
export function summarize(dots: { run_id: number; outcome: string; t: Date }[]): string {
  if (dots.length === 0) return "No runs recorded for this case.";
  const last = dots[dots.length - 1];
  const passed = dots.filter((d) => d.outcome === "pass");
  if (last.outcome === "pass") {
    let i = dots.length - 1;
    while (i > 0 && dots[i - 1].outcome === "pass") i--;
    const since = dots[i];
    return i === 0
      ? `Passing in all ${dots.length} run${dots.length === 1 ? "" : "s"} (since run #${since.run_id}, ${fmt(since.t)} UTC).`
      : `Passing since run #${since.run_id} (${fmt(since.t)} UTC); it was ${dots[i - 1].outcome} in run #${dots[i - 1].run_id}.`;
  }
  if (passed.length === 0) return `Never passed in ${dots.length} run${dots.length === 1 ? "" : "s"}; currently ${last.outcome}.`;
  const lp = passed[passed.length - 1];
  return `Currently ${last.outcome} (run #${last.run_id}); last passed in run #${lp.run_id} (${fmt(lp.t)} UTC).`;
}

let stop: (() => void) | undefined;

function clear(): void {
  stop?.();
  stop = undefined;
  el("history-canvas").replaceChildren();
}

// Opens the modal and plots the history of case eval/name in the experiment. onRun, when given, is called with a
// run ID when its dot is clicked.
export async function showCaseHistory(experimentID: number, experiment: string, evalName: string, name: string, onRun?: (runID: number) => void): Promise<void> {
  const modal = el<HTMLDialogElement>("history-modal");
  const status = el("history-status");
  el("history-title").textContent = `${evalName}/${name} — history (${experiment})`;
  status.className = "muted";
  status.textContent = "Loading…";
  clear();
  if (!modal.open) modal.showModal();
  try {
    const params = new URLSearchParams({ eval: evalName, name });
    const res = await fetch(`api/evals/experiments/${experimentID}/history?${params}`);
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    const data = (await res.json()) as History;
    if (!modal.open) return; // closed while loading
    const dots: Dot[] = (data.points ?? []).map((p) => ({
      ...p, outcome: outcomeOf(p), t: new Date(p.run_started_at), seconds: Math.max(0, (new Date(p.finished_at).getTime() - new Date(p.started_at).getTime()) / 1000) || 0,
    })).sort((a, b) => a.t.getTime() - b.t.getTime() || a.run_id - b.run_id);
    status.textContent = summarize(dots);
    if (dots.length) draw(el("history-canvas"), dots, onRun);
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load history: ${err instanceof Error ? err.message : String(err)}`;
  }
}

const M = { top: 28, right: 40, bottom: 44, left: 80 };
const PANEL_H = 280;

interface Panel {
  svg: d3.Selection<SVGSVGElement, undefined, null, undefined>;
  box: HTMLElement;
  x: d3.ScaleTime<number, number>;
  width: number;
}

// Adds a titled panel with the shared time axis; the caller draws the y axis and the data.
function panel(container: HTMLElement, title: string, domain: [Date, Date]): Panel {
  const box = document.createElement("section");
  box.className = "history-panel";
  const h = document.createElement("h3");
  h.textContent = title;
  box.append(h);
  container.append(box);
  const width = box.clientWidth || 900;
  const x = d3.scaleTime().domain(domain).nice().range([M.left, width - M.right]);
  const svg = d3.create("svg").attr("viewBox", [0, 0, width, PANEL_H]).attr("width", "100%").attr("height", PANEL_H);
  svg.append("g").attr("class", "history-axis").attr("transform", `translate(0,${PANEL_H - M.bottom})`)
    .call(d3.axisBottom(x).ticks(Math.max(2, Math.floor(width / 140))));
  svg.append("text").attr("class", "history-label").attr("x", (M.left + width - M.right) / 2).attr("y", PANEL_H - 8)
    .attr("text-anchor", "middle").text("run started (UTC)");
  box.append(svg.node() as SVGSVGElement);
  return { svg, box, x, width };
}

type Tip = (d: Dot) => string[];

// Draws circles for dots at (x, y) and wires the tooltip and the click-through to the run.
function addDots(p: Panel, dots: Dot[], y: (d: Dot) => number, fill: (d: Dot) => string, tip: Tip, onRun?: (runID: number) => void): void {
  const tipEl = d3.select(p.box).append("div").attr("class", "history-tip").style("display", "none");
  p.svg.append("g").selectAll("circle").data(dots).join("circle")
    .attr("class", "history-dot").attr("cx", (d) => p.x(d.t)).attr("cy", y).attr("r", 5.5).attr("fill", fill)
    .style("cursor", onRun ? "pointer" : "default")
    .on("mousemove", (ev: MouseEvent, d) => {
      const box = p.box.getBoundingClientRect();
      tipEl.style("display", null).style("left", `${ev.clientX - box.left + 12}px`).style("top", `${ev.clientY - box.top + 12}px`).text("");
      for (const l of tip(d)) tipEl.append("div").text(l);
    })
    .on("mouseleave", () => tipEl.style("display", "none"))
    .on("click", (_ev, d) => {
      if (!onRun) return;
      el<HTMLDialogElement>("history-modal").close();
      onRun(d.run_id);
    });
}

const baseTip = (d: Dot): string[] => [`run #${d.run_id} · ${d.outcome}`, `${fmt(d.t)} UTC`, `status ${d.status}${d.verdict ? `, verdict ${d.verdict}` : ""}${d.score != null ? `, score ${d.score}` : ""}`];
const colorOf = (d: Dot): string => COLORS[d.outcome] ?? OTHER_COLOR;

function draw(container: HTMLElement, dots: Dot[], onRun?: (runID: number) => void): void {
  // A single run (or all runs at the same instant) still needs a non-empty time domain.
  let [t0, t1] = d3.extent(dots, (d) => d.t) as [Date, Date];
  if (t0.getTime() === t1.getTime()) { t0 = new Date(t0.getTime() - 3600_000); t1 = new Date(t1.getTime() + 3600_000); }
  const domain: [Date, Date] = [t0, t1];
  const bottom = PANEL_H - M.bottom;

  // 1. Status over time.
  {
    const p = panel(container, "Status over time", domain);
    const lanes = [...LANES, ...new Set(dots.map((d) => d.outcome).filter((o) => !LANES.includes(o)))];
    const y = d3.scalePoint<string>().domain(lanes).range([M.top + 20, bottom - 20]);
    const yOf = (d: Dot): number => y(d.outcome) ?? 0;
    p.svg.append("g").selectAll("line").data(lanes).join("line")
      .attr("class", "history-lane").attr("x1", M.left).attr("x2", p.width - M.right)
      .attr("y1", (s) => y(s) ?? 0).attr("y2", (s) => y(s) ?? 0);
    p.svg.append("g").attr("class", "history-axis").attr("transform", `translate(${M.left},0)`).call(d3.axisLeft(y).tickSize(0).tickPadding(10));
    p.svg.append("path").attr("class", "history-line").attr("fill", "none")
      .attr("d", d3.line<Dot>().x((d) => p.x(d.t)).y(yOf).curve(d3.curveStepAfter)(dots));
    // Rule at each transition into passing: the moment it started to pass.
    dots.forEach((d, i) => {
      if (d.outcome !== "pass" || i === 0 || dots[i - 1].outcome === "pass") return;
      p.svg.append("line").attr("class", "history-fix").attr("x1", p.x(d.t)).attr("x2", p.x(d.t)).attr("y1", M.top).attr("y2", bottom);
      p.svg.append("text").attr("class", "history-label").attr("x", p.x(d.t) + 4).attr("y", M.top + 8).text(`passes from run #${d.run_id}`);
    });
    addDots(p, dots, yOf, colorOf, (d) => {
      const lines = baseTip(d);
      if (d.error) lines.push(`error: ${d.error}`);
      for (const c of d.problems ?? []) lines.push(`✗ ${c}`);
      return lines;
    }, onRun);
  }

  // 2. Latency over time.
  {
    const p = panel(container, "Latency over time", domain);
    const max = d3.max(dots, (d) => d.seconds) || 1;
    const y = d3.scaleLinear().domain([0, max]).nice().range([bottom, M.top]);
    p.svg.append("g").attr("class", "history-axis").attr("transform", `translate(${M.left},0)`).call(d3.axisLeft(y).ticks(5).tickFormat((v) => `${v} s`));
    p.svg.append("path").attr("class", "history-line").attr("fill", "none")
      .attr("d", d3.line<Dot>().x((d) => p.x(d.t)).y((d) => y(d.seconds))(dots));
    addDots(p, dots, (d) => y(d.seconds), colorOf, (d) => [...baseTip(d), `latency ${d.seconds.toFixed(2)} s`, `${d.steps} steps`], onRun);
  }

  // 3. Checks passed and failed over time, as two lines in the same plot.
  {
    const p = panel(container, "Checks passed / failed over time", domain);
    const nPass = (d: Dot): number => d.passed_checks?.length ?? 0;
    const nFail = (d: Dot): number => d.problems?.length ?? 0;
    const max = d3.max(dots, (d) => Math.max(nPass(d), nFail(d))) || 1;
    const y = d3.scaleLinear().domain([0, max]).nice().range([bottom, M.top]);
    p.svg.append("g").attr("class", "history-axis").attr("transform", `translate(${M.left},0)`)
      .call(d3.axisLeft(y).ticks(Math.min(5, Math.ceil(max))).tickFormat(d3.format("d")));
    const series: { label: string; color: string; n: (d: Dot) => number }[] = [
      { label: "passed", color: COLORS.pass, n: nPass }, { label: "failed", color: COLORS.fail, n: nFail },
    ];
    for (const s of series) {
      p.svg.append("path").attr("fill", "none").attr("stroke", s.color).attr("stroke-width", 2).attr("stroke-opacity", 0.85)
        .attr("d", d3.line<Dot>().x((d) => p.x(d.t)).y((d) => y(s.n(d)))(dots));
    }
    // Draw the failed dots first so the passed ones, and the lines, stay visible where the counts coincide.
    for (const s of [series[1], series[0]]) {
      addDots(p, dots, (d) => y(s.n(d)), () => s.color, (d) => [
        ...baseTip(d), `${nPass(d)} passed, ${nFail(d)} failed`, ...(d.problems ?? []).map((c) => `✗ ${c}`),
      ], onRun);
    }
    const legend = p.svg.append("g").attr("transform", `translate(${p.width - M.right - 120},${M.top - 14})`);
    series.forEach((s, i) => {
      legend.append("line").attr("x1", i * 64).attr("x2", i * 64 + 16).attr("y1", 0).attr("y2", 0).attr("stroke", s.color).attr("stroke-width", 2);
      legend.append("text").attr("class", "history-label").attr("x", i * 64 + 20).attr("y", 4).text(s.label);
    });
  }

  stop = () => container.replaceChildren();
}

export function initHistoryModal(): void {
  const modal = el<HTMLDialogElement>("history-modal");
  el("history-close").addEventListener("click", () => modal.close());
  modal.addEventListener("close", clear);
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
}
