// The "geo" view (see docs/geo.md): per database, how many entities are geo entities (they have coordinates in gb_entity_geo)
// and the 100 most connected ones. Clicking an entity opens a modal with an OpenStreetMap map (Leaflet, loaded as a global by
// index.html) of its place and of the geo entities near it, as the query pipeline selects them (api/geo/nearby).

declare const L: typeof import("leaflet");

interface GeoEntity {
  entity_id: number; name: string; type: string; user_id: string; dataset: string; connections: number;
  kind: string; latitude: number; longitude: number; display_name?: string; country?: string; country_code?: string;
  region?: string; place_type?: string; bbox?: [number, number, number, number]; importance?: number; provider?: string;
}
interface GeoView { entities: number; all_entities: number; top: GeoEntity[] }

interface NearbyEntity {
  entity_id: number; name: string; type: string; latitude: number; longitude: number; distance_km: number; anchor: string;
}
interface Nearby { radius_km: number; anchors: { name: string; latitude: number; longitude: number }[]; nearby: NearbyEntity[] }

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

function render(name: string, p: Panel, data: GeoView): void {
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
    tr.addEventListener("click", () => showMap(name, g));
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
    render(name, p, (await res.json()) as GeoView);
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
// The map modal: the entity, a circle of the search radius around it and the geo entities near it, which api/geo/nearby selects
// with the code of the proximity step of query. The map is Leaflet on the tiles of openstreetmap.org (the browser needs internet).

let map: import("leaflet").Map | null = null;
let loadId = 0; // a response of an earlier click or radius is dropped

function destroyMap(): void {
  loadId++;
  map?.remove();
  map = null;
  el("geo-map-box").replaceChildren();
}

const fmtKm = (km: number): string => `${km < 10 ? km.toFixed(1) : Math.round(km)} km`;

function showMap(db: string, g: GeoEntity, radiusKm?: number): void {
  const modal = el<HTMLDialogElement>("geo-modal");
  el("geo-title").textContent = g.name;
  el("geo-status").textContent = `${g.latitude.toFixed(5)}, ${g.longitude.toFixed(5)}`;
  if (!modal.open) modal.showModal();
  destroyMap();
  const mine = loadId;

  const box = el("geo-map-box");
  const m = L.map(box, { worldCopyJump: true }).setView([g.latitude, g.longitude], g.kind === "country" ? 5 : g.kind === "region" ? 7 : 11);
  map = m;
  L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19, attribution: '&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">OpenStreetMap</a> contributors',
  }).addTo(m);
  const anchor = L.circleMarker([g.latitude, g.longitude], { radius: 9, color: "#7f1d1d", weight: 2, fillColor: "#ef4444", fillOpacity: 0.9 }).addTo(m);
  anchor.bindTooltip(g.name, { permanent: true, direction: "top", offset: [0, -8] });
  let box_: import("leaflet").Rectangle | undefined;
  if (g.bbox) { // the bounding box of the place itself
    const [s, w, n, e] = g.bbox;
    box_ = L.rectangle([[s, w], [n, e]], { color: "#ef4444", weight: 1, fillOpacity: 0.05 }).addTo(m);
    highlightOnHover(box_, { weight: 3, fillOpacity: 0.25 });
  }

  const side = el("geo-info");
  side.replaceChildren(info(g), node("p", "muted", "Looking for nearby geo entities…"));
  const params = new URLSearchParams({ db, user_id: g.user_id, dataset: g.dataset, entity_id: String(g.entity_id) });
  if (radiusKm !== undefined) params.set("radius_km", String(radiusKm));
  void (async () => {
    try {
      const res = await fetch(`api/geo/nearby?${params}`);
      if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
      const data = (await res.json()) as Nearby;
      if (mine !== loadId) return;
      drawNearby(m, db, g, data, anchor, box_);
      side.replaceChildren(info(g), nearbyPanel(m, db, g, data));
    } catch (err) {
      if (mine !== loadId) return;
      side.replaceChildren(info(g), node("p", "error", `Failed to load the nearby geo entities: ${errText(err)}`));
    }
  })();
  // the modal has its size only once it is open
  requestAnimationFrame(() => { if (mine === loadId) m.invalidateSize(); });
}

const markers = new Map<number, import("leaflet").CircleMarker>();

// While the pointer is over a shape, draw it with `over` (a stronger outline and fill); back to what it had when it leaves.
function highlightOnHover(shape: import("leaflet").Path, over: import("leaflet").PathOptions): void {
  const { weight, fillOpacity } = shape.options;
  shape.on("mouseover", () => shape.setStyle(over));
  shape.on("mouseout", () => shape.setStyle({ weight, fillOpacity }));
}

function drawNearby(m: import("leaflet").Map, db: string, g: GeoEntity, data: Nearby, anchor: import("leaflet").CircleMarker,
  box?: import("leaflet").Rectangle): void {
  markers.clear();
  const circle = L.circle([g.latitude, g.longitude], { radius: data.radius_km * 1000, color: "#2563eb", weight: 1, fillOpacity: 0.04 }).addTo(m);
  highlightOnHover(circle, { weight: 3, fillOpacity: 0.18 });
  circle.bindTooltip(`Search radius: ${fmtKm(data.radius_km)}. The geo entities inside this circle are the nearby ones.`, { sticky: true });
  const pts: [number, number][] = [[g.latitude, g.longitude]];
  for (const n of data.nearby) {
    const mk = L.circleMarker([n.latitude, n.longitude], { radius: 6, color: "#1e3a8a", weight: 1.5, fillColor: "#3b82f6", fillOpacity: 0.85 }).addTo(m);
    mk.bindTooltip(`${n.name} (${fmtKm(n.distance_km)})`);
    mk.on("click", () => showMap(db, { ...g, entity_id: n.entity_id, name: n.name, type: n.type, latitude: n.latitude, longitude: n.longitude,
      kind: "place", display_name: undefined, bbox: undefined, connections: 0 }, data.radius_km));
    markers.set(n.entity_id, mk);
    pts.push([n.latitude, n.longitude]);
  }
  box?.bringToFront(); // the circle covers the box: keep the box hoverable
  anchor.bringToFront();
  // fit the neighbours when there are any, else the whole search circle
  if (data.nearby.length > 0) m.fitBounds(L.latLngBounds(pts), { padding: [30, 30], maxZoom: 12 });
  else m.fitBounds(circle.getBounds(), { padding: [10, 10] });
}

function info(g: GeoEntity): HTMLElement {
  const props = node("dl", "props");
  const row = (k: string, v: string | undefined): void => { if (v) props.append(node("dt", "", k), node("dd", "", v)); };
  row("place", g.display_name);
  row("entity", `${g.name} (${g.type}) #${g.entity_id}`);
  row("dataset", `${g.user_id} / ${g.dataset}`);
  row("kind", g.place_type ? `${g.kind} (${g.place_type})` : g.kind);
  row("region", [g.region, g.country].filter(Boolean).join(", "));
  if (g.connections) row("connections", fmtInt(g.connections));
  row("resolved by", g.provider);
  const box = node("div");
  box.append(props);
  return box;
}

// The radius field and the list of the nearby entities, nearest first; a click on one centers the map on it.
function nearbyPanel(m: import("leaflet").Map, db: string, g: GeoEntity, data: Nearby): HTMLElement {
  const box = node("div", "geo-nearby");
  box.appendChild(node("h3", "", "Nearby geo entities"));
  const form = box.appendChild(node("form", "geo-radius"));
  const label = form.appendChild(node("label", "", "radius (km) "));
  const input = label.appendChild(node("input"));
  input.type = "number";
  input.min = "0.1";
  input.max = "20015";
  input.step = "any";
  input.value = String(data.radius_km);
  form.appendChild(node("button", "", "Search")).type = "submit";
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const km = Number(input.value);
    if (km > 0 && km <= 20015) showMap(db, g, km);
  });
  box.appendChild(node("p", "muted", data.nearby.length === 0
    ? `No geo entities within ${fmtKm(data.radius_km)}.`
    : `${data.nearby.length} within ${fmtKm(data.radius_km)} (the ones the query pipeline gives its LLM as context; at most 30).`));
  const list = box.appendChild(node("ul", "geo-near-list"));
  for (const n of data.nearby) {
    const li = list.appendChild(node("li"));
    li.title = `Show ${n.name} on the map`;
    li.append(node("span", "", n.name), node("span", "muted", ` ${n.type} · ${fmtKm(n.distance_km)}`));
    li.addEventListener("click", () => {
      m.setView([n.latitude, n.longitude], Math.max(m.getZoom(), 12));
      markers.get(n.entity_id)?.openTooltip();
    });
  }
  return box;
}

export function initGeo(): void {
  const modal = el<HTMLDialogElement>("geo-modal");
  const close = (): void => { modal.close(); destroyMap(); };
  el("geo-close").addEventListener("click", close);
  modal.addEventListener("click", (e) => { if (e.target === modal) close(); });
  modal.addEventListener("cancel", destroyMap);
  el("geo-refresh").addEventListener("click", () => void load());
  void load();
}
