// The "geo" view (see docs/geo.md): per database, how many entities are geo entities (they have coordinates in gb_entity_geo)
// and the 100 most connected ones. Clicking an entity opens a modal with its place on an OpenStreetMap map.

interface GeoEntity {
  entity_id: number; name: string; type: string; user_id: string; dataset: string; connections: number;
  kind: string; latitude: number; longitude: number; display_name?: string; country?: string; country_code?: string;
  region?: string; place_type?: string; bbox?: [number, number, number, number]; importance?: number; provider?: string;
}
interface GeoView { entities: number; all_entities: number; top: GeoEntity[] }

interface Panel { status: HTMLElement; stats: HTMLElement; table: HTMLTableElement; body: HTMLTableSectionElement }

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

const fmtInt = (n: number): string => n.toLocaleString("en-US");
const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));

function tile(label: string, value: string, title = ""): HTMLElement {
  const d = node("div");
  if (title) d.title = title;
  d.append(node("dt", "", label), node("dd", "", value));
  return d;
}

const panels = new Map<string, Panel>();

function makePanel(name: string): { box: HTMLElement; panel: Panel } {
  const box = node("div", "geo-db");
  box.appendChild(node("h3", "", `${name} database`));
  const stats = box.appendChild(node("dl", "stats"));
  const status = box.appendChild(node("p", "muted"));
  const scroll = box.appendChild(node("div", "table-scroll"));
  const table = scroll.appendChild(node("table", "clickable"));
  table.hidden = true;
  const head = table.createTHead().insertRow();
  for (const [label, num] of [["#", true], ["Entity", false], ["Type", false], ["Kind", false], ["Place", false], ["Country", false],
    ["Dataset", false], ["Connections", true]] as [string, boolean][]) {
    head.appendChild(node("th", num ? "num" : "", label));
  }
  const body = table.createTBody();
  return { box, panel: { status, stats, table, body } };
}

function render(p: Panel, data: GeoView): void {
  const share = data.all_entities > 0 ? `${((100 * data.entities) / data.all_entities).toFixed(1)}%` : "-";
  p.stats.replaceChildren(
    tile("Geo entities", fmtInt(data.entities), "Entities of live datasets that are places and have coordinates"),
    tile("All entities", fmtInt(data.all_entities)),
    tile("Geo share", share, "Geo entities as a share of all entities"),
  );
  p.body.replaceChildren();
  if (data.top.length === 0) {
    p.status.textContent = data.entities === 0
      ? "No geo entities. Turn geo on (geo = true, or serve --geo) so that remember stores the coordinates of places."
      : "No geo entities in this database.";
    p.table.hidden = true;
    return;
  }
  p.status.textContent = `The ${data.top.length} most connected geo entities (live facts, as source or target). Click one to see it on a map.`;
  data.top.forEach((g, i) => {
    const tr = p.body.insertRow();
    tr.title = `Show ${g.name} on a map`;
    tr.insertCell().textContent = String(i + 1);
    tr.cells[0].className = "num";
    tr.insertCell().textContent = g.name;
    tr.insertCell().textContent = g.type;
    tr.insertCell().textContent = g.kind;
    tr.insertCell().textContent = g.display_name || `${g.latitude.toFixed(4)}, ${g.longitude.toFixed(4)}`;
    tr.insertCell().textContent = g.country || "";
    tr.insertCell().textContent = `${g.user_id} / ${g.dataset}`;
    const c = tr.insertCell();
    c.className = "num";
    c.textContent = fmtInt(g.connections);
    tr.addEventListener("click", () => showMap(g));
  });
  p.table.hidden = false;
}

async function loadDatabase(name: string): Promise<void> {
  const p = panels.get(name);
  if (!p) return;
  p.status.className = "muted";
  p.status.textContent = "Loading…";
  try {
    const res = await fetch(`api/geo?${new URLSearchParams({ db: name })}`);
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    render(p, (await res.json()) as GeoView);
  } catch (err) {
    p.status.className = "error";
    p.status.textContent = `Failed to load geo entities: ${errText(err)}`;
  }
}

async function load(): Promise<void> {
  const root = el("geo-content");
  if (panels.size === 0) {
    try {
      const res = await fetch("api/databases");
      if (!res.ok) throw new Error(`${res.status}`);
      const names = ((await res.json()) as { databases: string[] }).databases;
      for (const name of names) {
        const { box, panel } = makePanel(name);
        panels.set(name, panel);
        root.appendChild(box);
      }
    } catch (err) {
      root.textContent = `Failed to list databases: ${errText(err)}`;
      return;
    }
  }
  await Promise.all([...panels.keys()].map((n) => loadDatabase(n)));
}

//---------------------------------------------------------------------------------------------
// The map modal: the OpenStreetMap embed (export/embed.html) shows the bounding box of the place with a marker on it.

const clamp = (v: number, lo: number, hi: number): number => Math.min(hi, Math.max(lo, v));

// The area the embedded map shows: the place's bounding box, or a small square around the point if it has none or is tiny.
export function mapBounds(g: Pick<GeoEntity, "latitude" | "longitude" | "bbox">): [number, number, number, number] {
  const MIN = 0.02; // degrees, about 2 km: a point alone gets a street-level view
  let [s, w, n, e] = g.bbox ?? [g.latitude, g.longitude, g.latitude, g.longitude];
  if (n - s < MIN) { const mid = (n + s) / 2; s = mid - MIN / 2; n = mid + MIN / 2; }
  if (e - w < MIN) { const mid = (e + w) / 2; w = mid - MIN / 2; e = mid + MIN / 2; }
  return [clamp(s, -85, 85), clamp(w, -180, 180), clamp(n, -85, 85), clamp(e, -180, 180)];
}

function showMap(g: GeoEntity): void {
  const modal = el<HTMLDialogElement>("geo-modal");
  el("geo-title").textContent = g.name;
  el("geo-status").textContent = `${g.latitude.toFixed(5)}, ${g.longitude.toFixed(5)}`;

  const [s, w, n, e] = mapBounds(g);
  const lat = g.latitude.toFixed(6);
  const lon = g.longitude.toFixed(6);
  const frame = node("iframe", "geo-map");
  frame.title = `${g.name} on OpenStreetMap`;
  frame.loading = "lazy";
  frame.referrerPolicy = "no-referrer";
  frame.src = `https://www.openstreetmap.org/export/embed.html?${new URLSearchParams({
    bbox: [w, s, e, n].join(","), layer: "mapnik", marker: `${lat},${lon}`,
  })}`;
  el("geo-map-box").replaceChildren(frame);

  const props = node("dl", "props");
  const row = (k: string, v: string | undefined): void => { if (v) props.append(node("dt", "", k), node("dd", "", v)); };
  row("place", g.display_name);
  row("entity", `${g.name} (${g.type}) #${g.entity_id}`);
  row("dataset", `${g.user_id} / ${g.dataset}`);
  row("kind", g.place_type ? `${g.kind} (${g.place_type})` : g.kind);
  row("region", [g.region, g.country].filter(Boolean).join(", "));
  row("connections", fmtInt(g.connections));
  row("resolved by", g.provider);
  const links = node("p", "geo-links");
  const a = node("a", "", "Open in OpenStreetMap");
  a.href = `https://www.openstreetmap.org/?mlat=${lat}&mlon=${lon}#map=${g.kind === "country" ? 5 : g.kind === "region" ? 7 : 13}/${lat}/${lon}`;
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  links.append(a);
  el("geo-info").replaceChildren(props, links);

  if (!modal.open) modal.showModal();
}

export function initGeo(): void {
  const modal = el<HTMLDialogElement>("geo-modal");
  const close = (): void => { modal.close(); el("geo-map-box").replaceChildren(); };
  el("geo-close").addEventListener("click", close);
  modal.addEventListener("click", (e) => { if (e.target === modal) close(); });
  modal.addEventListener("cancel", () => el("geo-map-box").replaceChildren());
  el("geo-refresh").addEventListener("click", () => void load());
  void load();
}
