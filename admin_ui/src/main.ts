// gligabao admin UI: a health indicator and a dataset list. Talks to the server's own HTTP API.

interface Dataset {
  name: string;
  description?: string;
  tags?: string[];
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

async function loadDatasets(userId: string, includeDeleted: boolean): Promise<void> {
  const status = $("datasets-status");
  const table = $<HTMLTableElement>("datasets-table");
  const body = table.tBodies[0];
  status.className = "muted";
  status.textContent = "Loading…";
  table.hidden = true;
  body.replaceChildren();

  const params = new URLSearchParams({ user_id: userId });
  if (includeDeleted) params.set("include_deleted", "true");

  try {
    const res = await fetch(`/v1/gb/datasets?${params}`);
    if (!res.ok) {
      const msg = await res.text();
      throw new Error(`${res.status}: ${msg.trim()}`);
    }
    const data: unknown = await res.json();
    const list: Dataset[] = Array.isArray(data) ? data : ((data as { datasets?: Dataset[] }).datasets ?? []);
    if (list.length === 0) {
      status.textContent = "No datasets.";
      return;
    }
    for (const ds of list) {
      const tr = body.insertRow();
      // textContent only: never inject server data as HTML.
      tr.insertCell().textContent = ds.name;
      tr.insertCell().textContent = ds.description ?? "";
      tr.insertCell().textContent = (ds.tags ?? []).join(", ");
    }
    status.textContent = `${list.length} dataset${list.length === 1 ? "" : "s"}.`;
    table.hidden = false;
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load datasets: ${err instanceof Error ? err.message : String(err)}`;
  }
}

$<HTMLFormElement>("datasets-form").addEventListener("submit", (e) => {
  e.preventDefault();
  void loadDatasets($<HTMLInputElement>("user-id").value.trim(), $<HTMLInputElement>("include-deleted").checked);
});

void checkHealth();
setInterval(() => void checkHealth(), 15000);
