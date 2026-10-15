// gligabao admin UI: a health indicator, system totals, the datasets of all users and and, in their own views, the evals (evals.ts), timeseries (timeseries.ts), model runs (modelruns.ts) and remember runs (rememberruns.ts). Talks to the server's own HTTP API.

import { initEvals } from "./evals.js";
import { initGraphModal, showGraph } from "./graph.js";
import { initHistoryModal } from "./history.js";
import { initImageOverlay } from "./image.js";
import { initImageStats } from "./imagestats.js";
import { initModelRuns } from "./modelruns.js";
import { initRememberRuns } from "./rememberruns.js";
import { initTimeseries } from "./timeseries.js";
import { initTraceModal } from "./trace.js";
import { initVectorsModal, showVectors } from "./vectors.js";
import { initWebSearch } from "./websearch.js";

interface Dataset {
  user_id: string;
  name: string;
  description?: string;
  tags?: string[];
  created_at: string;
  deleted: boolean;
  artifacts: number;
  entities: number;
  edges: number;
}

interface Overview {
  datasets: Dataset[];
  totals: { artifacts: number; entities: number; edges: number };
}

// "5 days ago" / "3 hours ago" / "12 mins ago" for an ISO timestamp (days, hours or minutes; "just now" under a minute).
function timeAgo(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const mins = Math.max(0, Math.floor((Date.now() - t) / 60000));
  const plural = (n: number, unit: string) => `${n} ${unit}${n === 1 ? "" : "s"} ago`;
  if (mins < 1) return "just now";
  if (mins < 60) return plural(mins, "min");
  if (mins < 1440) return plural(Math.floor(mins / 60), "hour");
  return plural(Math.floor(mins / 1440), "day");
}

function $<T extends HTMLElement>(id: string): T {
  const el = document.getElementById(id);
  if (!el) throw new Error(`missing element #${id}`);
  return el as T;
}

async function checkHealth(): Promise<void> {
  const badge = $("health");
  try {
    const res = await fetch("/health");
    const ok = res.ok;
    badge.textContent = ok ? "healthy" : "unhealthy";
    badge.className = `badge ${ok ? "badge-ok" : "badge-error"}`;
  } catch {
    badge.textContent = "unreachable";
    badge.className = "badge badge-error";
  }
}

// Shows the running version and a link to its GitHub commit in the lower right corner.
async function showVersion(): Promise<void> {
  try {
    const res = await fetch("/version");
    if (!res.ok) return;
    const lib = (await res.json()).gligabao as { version?: string; commit?: string; commit_url?: string };
    if (!lib?.version) return;
    const el = $("version");
    el.textContent = `v${lib.version}`;
    if (lib.commit_url) {
      el.append(" · ");
      const a = document.createElement("a");
      a.href = lib.commit_url;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      a.title = lib.commit ?? "";
      a.textContent = "github url";
      el.append(a);
    }
    el.hidden = false;
  } catch {
    // version is informational only
  }
}

const COLUMNS: [string, boolean][] = [
  ["User", false], ["Name", false], ["Description", false], ["Tags", false], ["Created", false],
  ["Artifacts", true], ["Entities", true], ["Edges", true], ["Actions", false],
];

// Builds the card of one database (system totals + datasets table) and returns the parts that get filled in later.
function makeSection(name: string) {
  const card = document.createElement("section");
  card.className = "card";
  const h = document.createElement("h2");
  h.textContent = `${name} database`;
  const stats = document.createElement("dl");
  stats.className = "stats";
  const totals: Record<string, HTMLElement> = {};
  for (const label of ["Artifacts", "Entities", "Edges"]) {
    const div = stats.appendChild(document.createElement("div"));
    div.appendChild(document.createElement("dt")).textContent = label;
    totals[label.toLowerCase()] = div.appendChild(document.createElement("dd"));
    totals[label.toLowerCase()].textContent = "–";
  }
  const status = document.createElement("p");
  status.className = "muted";
  const table = document.createElement("table");
  table.hidden = true;
  const head = table.createTHead().insertRow();
  for (const [label, num] of COLUMNS) {
    const th = document.createElement("th");
    th.textContent = label;
    if (num) th.className = "num";
    head.appendChild(th);
  }
  const body = table.createTBody();
  card.append(h, stats, status, table);
  return { card, totals, status, table, body };
}

async function loadOverview(name: string, s: ReturnType<typeof makeSection>, includeDeleted: boolean): Promise<void> {
  s.status.className = "muted";
  s.status.textContent = "Loading…";
  s.table.hidden = true;
  s.body.replaceChildren();

  const params = new URLSearchParams({ db: name });
  if (includeDeleted) params.set("include_deleted", "true");

  try {
    const res = await fetch(`api/overview?${params}`);
    if (!res.ok) {
      const msg = await res.text();
      throw new Error(`${res.status}: ${msg.trim()}`);
    }
    const data = (await res.json()) as Overview;
    s.totals.artifacts.textContent = String(data.totals.artifacts);
    s.totals.entities.textContent = String(data.totals.entities);
    s.totals.edges.textContent = String(data.totals.edges);

    const list = data.datasets ?? [];
    if (list.length === 0) {
      s.status.textContent = "No datasets.";
      return;
    }
    for (const ds of list) {
      const tr = s.body.insertRow();
      // textContent only: never inject server data as HTML.
      tr.insertCell().textContent = ds.user_id;
      tr.insertCell().textContent = ds.deleted ? `${ds.name} (deleted)` : ds.name;
      tr.insertCell().textContent = ds.description ?? "";
      tr.insertCell().textContent = (ds.tags ?? []).join(", ");
      const created = tr.insertCell();
      created.textContent = timeAgo(ds.created_at);
      created.title = ds.created_at;
      for (const n of [ds.artifacts, ds.entities, ds.edges]) {
        const td = tr.insertCell();
        td.className = "num";
        td.textContent = String(n);
      }
      const graphCell = tr.insertCell();
      if (!ds.deleted) {
        const btn = graphCell.appendChild(document.createElement("button"));
        btn.type = "button";
        btn.className = "small";
        btn.classList.add("icon");
        btn.title = "View graph";
        btn.setAttribute("aria-label", `View graph of dataset ${ds.name}`);
        btn.innerHTML = GRAPH_ICON; // static markup, no server data
        btn.addEventListener("click", () => void showGraph(name, ds.user_id, ds.name));

        const vec = graphCell.appendChild(document.createElement("button"));
        vec.type = "button";
        vec.className = "small";
        vec.classList.add("icon");
        vec.title = "View vec db";
        vec.setAttribute("aria-label", `View vec db of dataset ${ds.name}`);
        vec.innerHTML = VECTORS_ICON; // static markup, no server data
        vec.addEventListener("click", () => void showVectors(name, ds.user_id, ds.name));

        const del = graphCell.appendChild(document.createElement("button"));
        del.type = "button";
        del.className = "small danger";
        del.title = "Delete dataset";
        del.setAttribute("aria-label", `Delete dataset ${ds.name}`);
        del.innerHTML = TRASH_ICON; // static markup, no server data

        // Two-step delete without a dialog: the trash button reveals a "?" button right next to it, which confirms.
        const confirmBtn = graphCell.appendChild(document.createElement("button"));
        confirmBtn.type = "button";
        confirmBtn.className = "small danger";
        confirmBtn.title = "Confirm delete";
        confirmBtn.setAttribute("aria-label", `Confirm delete of dataset ${ds.name}`);
        confirmBtn.textContent = "?";
        confirmBtn.hidden = true;
        del.addEventListener("click", () => {
          confirmBtn.hidden = !confirmBtn.hidden;
        });
        // Moving away from the row without confirming cancels.
        tr.addEventListener("mouseleave", () => {
          confirmBtn.hidden = true;
        });
        confirmBtn.addEventListener("click", () => {
          del.disabled = true;
          confirmBtn.disabled = true;
          void deleteDataset(name, ds.user_id, ds.name, s, tr);
        });
      }
    }
    s.status.textContent = `${list.length} dataset${list.length === 1 ? "" : "s"}.`;
    s.table.hidden = false;
  } catch (err) {
    s.status.className = "error";
    s.status.textContent = `Failed to load datasets: ${err instanceof Error ? err.message : String(err)}`;
  }
}

const FADE_OUT_MS = 500; // keep in sync with tr.removing in styles.scss

const ICON_ATTRS =
  'viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" ' +
  'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"';

// Three connected nodes: the knowledge graph.
const GRAPH_ICON =
  `<svg ${ICON_ATTRS}><circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="8" r="2.5"/><circle cx="12" cy="19" r="2.5"/>` +
  '<path d="M8.3 7l7.4.6"/><path d="M7.2 8.2l3.6 8.5"/><path d="M16.8 10.2l-3.6 6.5"/></svg>';

// A database cylinder with a scatter of points: the vector database.
const VECTORS_ICON =
  `<svg ${ICON_ATTRS}><ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5"/>` +
  '<path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/></svg>';

const TRASH_ICON =
  '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" ' +
  'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M19 6l-1 14H6L5 6"/><path d="M10 11v6"/><path d="M14 11v6"/></svg>';

// Soft-deletes a dataset and reloads the database's table, so it disappears from the list (it stays, marked
// "(deleted)", only while "include deleted datasets" is ticked) and the totals are updated.
async function deleteDataset(db: string, userID: string, dataset: string, s: ReturnType<typeof makeSection>, row: HTMLTableRowElement): Promise<void> {
  const params = new URLSearchParams({ db, user_id: userID, dataset });
  let failure = "";
  try {
    const res = await fetch(`api/datasets?${params}`, { method: "DELETE" });
    // 404: already gone (e.g. deleted elsewhere); the reload shows the current state.
    if (!res.ok && res.status !== 404) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
  } catch (err) {
    failure = `Failed to delete dataset "${dataset}": ${err instanceof Error ? err.message : String(err)}`;
  }
  // Fade the row out (0.5s, see tr.removing) before the reload drops it; with deleted datasets shown it stays.
  if (!failure && !includeDeleted()) {
    row.classList.add("removing");
    await new Promise((resolve) => setTimeout(resolve, FADE_OUT_MS));
  }
  await loadOverview(db, s, includeDeleted());
  if (failure) {
    s.status.className = "error";
    s.status.textContent = failure;
  }
}

const sections = new Map<string, ReturnType<typeof makeSection>>();

// Lists the databases the server offers (standard, plus eval when configured) and loads each.
async function loadAll(includeDeleted: boolean): Promise<void> {
  const container = $("databases");
  if (sections.size === 0) {
    try {
      const res = await fetch("api/databases");
      if (!res.ok) throw new Error(`${res.status}`);
      const names = ((await res.json()) as { databases: string[] }).databases;
      for (const name of names) {
        const s = makeSection(name);
        sections.set(name, s);
        container.appendChild(s.card);
      }
    } catch (err) {
      container.textContent = `Failed to list databases: ${err instanceof Error ? err.message : String(err)}`;
      return;
    }
  }
  await Promise.all([...sections].map(([name, s]) => loadOverview(name, s, includeDeleted)));
}

const includeDeleted = (): boolean => $<HTMLInputElement>("include-deleted").checked;

$<HTMLFormElement>("datasets-form").addEventListener("submit", (e) => {
  e.preventDefault();
  void loadAll(includeDeleted());
});
$("include-deleted").addEventListener("change", () => void loadAll(includeDeleted()));

// Switches between the admin views (main, evals, timeseries, model runs, remember runs); the choice is kept in the URL hash.
function initViews(): void {
  const buttons = [...document.querySelectorAll<HTMLButtonElement>(".view-btn")];
  const views = [...document.querySelectorAll<HTMLElement>(".view")];
  const show = (name: string): void => {
    if (!views.some((v) => v.dataset.view === name)) name = "main";
    for (const v of views) v.hidden = v.dataset.view !== name;
    for (const b of buttons) {
      const on = b.dataset.view === name;
      b.classList.toggle("active", on);
      b.setAttribute("aria-pressed", String(on));
    }
  };
  for (const b of buttons) {
    b.addEventListener("click", () => {
      const name = b.dataset.view ?? "main";
      show(name);
      history.replaceState(null, "", name === "main" ? location.pathname + location.search : `#${name}`);
    });
  }
  show(location.hash.slice(1));
}

initViews();
initGraphModal();
initVectorsModal();
initTraceModal();
initHistoryModal();
initEvals();
initTimeseries();
initModelRuns();
initRememberRuns();
initImageOverlay();
initImageStats();
initWebSearch();
void loadAll(false);
void checkHealth();
void showVersion();
setInterval(() => void checkHealth(), 15000);
