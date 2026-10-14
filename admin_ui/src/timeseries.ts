// The "timeseries" view: the series stored in each database, across all datasets of all users. Per series: the period
// it covers and its length (minutes, hours or days), the entities it belongs to, its anchors, and its other parameters
// and stats.

interface Stats {
  first: number;
  last: number;
  min: number;
  max: number;
  mean: number;
  change: number;
  pct_change?: number;
  trend?: string;
}
interface Anchor {
  type: string;
  id: number;
  label: string;
  role?: string;
  window_start?: string;
  window_end?: string;
}
interface Entity { id: number; name: string; type: string; via: string }
interface Series {
  id: number;
  user_id: string;
  dataset: string;
  key: string;
  field: string;
  name: string;
  unit?: string;
  frequency?: string;
  source: string;
  provider?: string;
  definition?: unknown;
  point_count: number;
  first_ts?: string;
  last_ts?: string;
  stats?: Stats;
  created_at: string;
  updated_at: string;
  entities: Entity[];
  anchors: Anchor[];
  preview?: [number, number][]; // [unix ms, value], downsampled
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

const plural = (n: number, unit: string): string => `${n} ${unit}${n === 1 ? "" : "s"}`;

function num(n: number): string {
  return Number.isInteger(n) ? String(n) : String(Number(n.toPrecision(6)));
}

// "2025-01-31 09:00" (UTC), the seconds only when there are some.
function formatTS(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const s = d.toISOString();
  return `${s.slice(0, 10)} ${s.slice(11, d.getUTCSeconds() === 0 ? 16 : 19)}`;
}

// The length of a period, in the largest of minutes | hours | days that fits it: "45 minutes", "3 hours", "30 days".
// A fraction is kept only below ten of the unit ("1.5 hours"). A period of a single instant has length 0 minutes.
function formatLength(ms: number): { text: string; title: string } {
  const mins = ms / 60000;
  const fmt = (v: number, unit: string): string => {
    const r = v < 10 ? Math.round(v * 10) / 10 : Math.round(v);
    return `${r} ${unit}${r === 1 ? "" : "s"}`;
  };
  let text: string;
  if (mins < 60) text = fmt(mins, "minute");
  else if (mins < 2880) text = fmt(mins / 60, "hour");
  else text = fmt(mins / 1440, "day");
  const title = `${num(Math.round(mins * 100) / 100)} minutes = ${num(Math.round((mins / 60) * 100) / 100)} hours = ${num(Math.round((mins / 1440) * 1000) / 1000)} days`;
  return { text, title };
}

function period(start?: string, end?: string): HTMLElement | null {
  if (!start || !end) return null;
  const ms = Date.parse(end) - Date.parse(start);
  if (Number.isNaN(ms)) return null;
  const len = formatLength(ms);
  const box = node("span");
  box.append(`${formatTS(start)} → ${formatTS(end)} UTC `);
  const l = node("strong", "", `(${len.text})`);
  l.title = len.title;
  box.append(l);
  return box;
}

function prop(dl: HTMLElement, label: string, value: string | HTMLElement | null | undefined, title = ""): void {
  if (value === null || value === undefined || value === "") return;
  dl.appendChild(node("dt", "", label));
  const dd = dl.appendChild(node("dd"));
  if (typeof value === "string") dd.textContent = value;
  else dd.appendChild(value);
  if (title) dd.title = title;
}

function chip(text: string, title = "", cls = ""): HTMLElement {
  const c = node("span", `chip ${cls}`.trim(), text);
  if (title) c.title = title;
  return c;
}

const VIA: Record<string, string> = {
  anchor: "anchored to this entity",
  edge: "endpoint of a fact the series is anchored to",
  artifact: "mentioned by a text the series is anchored to",
};

function statsLine(s: Stats, unit: string): string {
  const u = unit ? ` ${unit}` : "";
  const pct = s.pct_change === undefined ? "" : ` (${s.pct_change >= 0 ? "+" : ""}${s.pct_change.toFixed(2)}%)`;
  const arrow = s.trend === "up" ? "▲" : s.trend === "down" ? "▼" : "▬";
  return `${arrow} ${num(s.first)}${u} → ${num(s.last)}${u}${pct}`;
}

const SVG = "http://www.w3.org/2000/svg";

// A small line plot of the (downsampled) points, colored by the trend; hovering shows the range.
function sparkline(s: Series): SVGSVGElement | null {
  const pts = s.preview ?? [];
  if (pts.length === 0) return null;
  const w = 160;
  const h = 44;
  const pad = 3;
  const t0 = pts[0][0];
  const t1 = pts[pts.length - 1][0];
  let lo = Infinity;
  let hi = -Infinity;
  for (const [, v] of pts) {
    lo = Math.min(lo, v);
    hi = Math.max(hi, v);
  }
  const x = (t: number): number => (t1 === t0 ? w / 2 : pad + ((t - t0) / (t1 - t0)) * (w - 2 * pad));
  const y = (v: number): number => (hi === lo ? h / 2 : h - pad - ((v - lo) / (hi - lo)) * (h - 2 * pad));
  const svg = document.createElementNS(SVG, "svg");
  svg.setAttribute("viewBox", `0 0 ${w} ${h}`);
  svg.setAttribute("width", String(w));
  svg.setAttribute("height", String(h));
  svg.setAttribute("class", `ts-spark ts-spark-${s.stats?.trend ?? "flat"}`);
  svg.setAttribute("role", "img");
  const title = document.createElementNS(SVG, "title");
  const u = s.unit ? ` ${s.unit}` : "";
  title.textContent = `${s.name || s.key}: ${plural(s.point_count, "point")}, ${num(lo)}${u} – ${num(hi)}${u}`;
  svg.appendChild(title);
  if (pts.length === 1) {
    const c = document.createElementNS(SVG, "circle");
    c.setAttribute("cx", String(x(pts[0][0])));
    c.setAttribute("cy", String(y(pts[0][1])));
    c.setAttribute("r", "2");
    svg.appendChild(c);
    return svg;
  }
  const path = document.createElementNS(SVG, "path");
  path.setAttribute("d", pts.map(([t, v], i) => `${i === 0 ? "M" : "L"}${x(t).toFixed(1)},${y(v).toFixed(1)}`).join(""));
  svg.appendChild(path);
  return svg;
}

function seriesItem(s: Series): HTMLElement {
  const item = node("div", "ts-item");

  const head = item.appendChild(node("div", "ts-head"));
  head.appendChild(node("strong", "", s.name || s.key));
  head.appendChild(node("code", "", s.field && s.field !== "value" ? `${s.key} · ${s.field}` : s.key));
  const src = s.source === "fetched" && s.provider ? `fetched · ${s.provider}` : s.source;
  head.appendChild(chip(src, "where the points came from", s.source === "fetched" ? "chip-web" : ""));
  if (s.frequency) head.appendChild(chip(s.frequency, "frequency label"));

  const body = item.appendChild(node("div", "ts-body"));
  const dl = body.appendChild(node("dl", "props"));
  const per = period(s.first_ts, s.last_ts);
  prop(dl, "Period", per ?? "no points");
  const entities = node("div", "ws-chips");
  for (const e of s.entities) entities.appendChild(chip(e.via === "anchor" ? `${e.name} (${e.type})` : `${e.name} (${e.type}) · via ${e.via}`, VIA[e.via] ?? e.via));
  prop(dl, "Entities", s.entities.length > 0 ? entities : "none (anchored to no entity)");
  prop(dl, "Dataset", `${s.user_id} / ${s.dataset}`);
  prop(dl, "Points", String(s.point_count));
  prop(dl, "Unit", s.unit);
  if (s.stats) {
    const st = s.stats;
    prop(dl, "Change", statsLine(st, s.unit ?? ""));
    prop(dl, "Min / max / mean", `${num(st.min)} / ${num(st.max)} / ${num(st.mean)}${s.unit ? " " + s.unit : ""}`);
  }
  prop(dl, "Updated", formatTS(s.updated_at) + " UTC", `created ${s.created_at}`);
  prop(dl, "ID", String(s.id));
  const spark = sparkline(s);
  if (spark) body.appendChild(spark); // on the right of the properties

  if (s.anchors.length > 0) {
    const d = item.appendChild(node("details"));
    d.appendChild(node("summary", "", plural(s.anchors.length, "anchor")));
    const ul = d.appendChild(node("ul", "ts-anchors"));
    for (const a of s.anchors) {
      const li = ul.appendChild(node("li"));
      li.appendChild(chip(a.type));
      li.append(` ${a.label || `#${a.id} (not found)`}`);
      if (a.role) li.append(` · role: ${a.role}`);
      const w = period(a.window_start, a.window_end);
      if (w) {
        li.append(" · window: ");
        li.appendChild(w);
      } else {
        li.append(" · whole series");
      }
    }
  }
  if (s.definition !== undefined) {
    const d = item.appendChild(node("details"));
    d.appendChild(node("summary", "", "fetch definition"));
    d.appendChild(node("pre", "ts-def", JSON.stringify(s.definition, null, 2)));
  }
  return item;
}

async function loadDatabase(name: string, status: HTMLElement, list: HTMLElement): Promise<void> {
  status.className = "muted";
  status.textContent = "Loading…";
  list.replaceChildren();
  try {
    const res = await fetch(`api/timeseries?${new URLSearchParams({ db: name })}`);
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    const series = ((await res.json()) as { series: Series[] }).series ?? [];
    status.textContent = series.length === 0 ? "No timeseries." : plural(series.length, "timeseries") + ".";
    for (const s of series) list.appendChild(seriesItem(s));
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load timeseries: ${err instanceof Error ? err.message : String(err)}`;
  }
}

let loaded = false;

async function load(): Promise<void> {
  const root = el("timeseries-content");
  if (!loaded) {
    try {
      const res = await fetch("api/databases");
      if (!res.ok) throw new Error(`${res.status}`);
      const names = ((await res.json()) as { databases: string[] }).databases;
      for (const name of names) {
        const box = root.appendChild(node("div", "ts-db"));
        box.appendChild(node("h3", "", `${name} database`));
        const status = box.appendChild(node("p", "muted"));
        const list = box.appendChild(node("div", "ts-list"));
        dbs.set(name, { status, list });
      }
      loaded = true;
    } catch (err) {
      root.textContent = `Failed to list databases: ${err instanceof Error ? err.message : String(err)}`;
      return;
    }
  }
  await Promise.all([...dbs].map(([name, p]) => loadDatabase(name, p.status, p.list)));
}

const dbs = new Map<string, { status: HTMLElement; list: HTMLElement }>();

export function initTimeseries(): void {
  el("timeseries-refresh").addEventListener("click", () => void load());
  void load();
}
