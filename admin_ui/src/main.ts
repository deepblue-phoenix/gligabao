// gligabao admin UI: a health indicator, system totals and the datasets of all users. Talks to the server's own HTTP API.

interface Dataset {
  user_id: string;
  name: string;
  description?: string;
  tags?: string[];
  deleted: boolean;
  artifacts: number;
  entities: number;
  edges: number;
}

interface Overview {
  datasets: Dataset[];
  totals: { artifacts: number; entities: number; edges: number };
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

const COLUMNS: [string, boolean][] = [
  ["User", false], ["Name", false], ["Description", false], ["Tags", false],
  ["Artifacts", true], ["Entities", true], ["Edges", true],
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
      for (const n of [ds.artifacts, ds.entities, ds.edges]) {
        const td = tr.insertCell();
        td.className = "num";
        td.textContent = String(n);
      }
    }
    s.status.textContent = `${list.length} dataset${list.length === 1 ? "" : "s"}.`;
    s.table.hidden = false;
  } catch (err) {
    s.status.className = "error";
    s.status.textContent = `Failed to load datasets: ${err instanceof Error ? err.message : String(err)}`;
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

void loadAll(false);
void checkHealth();
setInterval(() => void checkHealth(), 15000);
