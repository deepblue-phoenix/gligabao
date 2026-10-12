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

async function loadOverview(includeDeleted: boolean): Promise<void> {
  const status = $("datasets-status");
  const table = $<HTMLTableElement>("datasets-table");
  const body = table.tBodies[0];
  status.className = "muted";
  status.textContent = "Loading…";
  table.hidden = true;
  body.replaceChildren();

  const params = new URLSearchParams();
  if (includeDeleted) params.set("include_deleted", "true");

  try {
    const res = await fetch(`api/overview?${params}`);
    if (!res.ok) {
      const msg = await res.text();
      throw new Error(`${res.status}: ${msg.trim()}`);
    }
    const data = (await res.json()) as Overview;
    $("total-artifacts").textContent = String(data.totals.artifacts);
    $("total-entities").textContent = String(data.totals.entities);
    $("total-edges").textContent = String(data.totals.edges);

    const list = data.datasets ?? [];
    if (list.length === 0) {
      status.textContent = "No datasets.";
      return;
    }
    for (const ds of list) {
      const tr = body.insertRow();
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
    status.textContent = `${list.length} dataset${list.length === 1 ? "" : "s"}.`;
    table.hidden = false;
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load datasets: ${err instanceof Error ? err.message : String(err)}`;
  }
}

const includeDeleted = (): boolean => $<HTMLInputElement>("include-deleted").checked;

$<HTMLFormElement>("datasets-form").addEventListener("submit", (e) => {
  e.preventDefault();
  void loadOverview(includeDeleted());
});
$("include-deleted").addEventListener("change", () => void loadOverview(includeDeleted()));

void loadOverview(false);
void checkHealth();
setInterval(() => void checkHealth(), 15000);
