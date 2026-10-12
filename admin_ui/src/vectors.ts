// Vector DB statistics of a dataset (one row per vector index), shown in a modal.

interface IndexStats {
  index: string;
  vector_dataset: string;
  expected: number; // rows (entities / edges) that should have a vector
  backend: string;
  count: number;
  dimensions: number;
  bytes: number;
}
interface VectorStats { backend: string; indexes: IndexStats[] }

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = n;
  let i = -1;
  do { v /= 1024; i++; } while (v >= 1024 && i < units.length - 1);
  return `${v.toFixed(v < 10 ? 2 : 1)} ${units[i]}`;
}

// Opens the modal and shows the vector statistics of a live dataset (fetched from the admin API).
export async function showVectors(db: string, userID: string, dataset: string): Promise<void> {
  const modal = el<HTMLDialogElement>("vectors-modal");
  const status = el("vectors-status");
  const content = el("vectors-content");
  el("vectors-title").textContent = `Vector DB: ${userID} / ${dataset}`;
  status.className = "muted";
  status.textContent = "Loading…";
  content.replaceChildren();
  if (!modal.open) modal.showModal();

  try {
    const params = new URLSearchParams({ db, user_id: userID, dataset });
    const res = await fetch(`api/vectors?${params}`);
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    const data = (await res.json()) as VectorStats;
    if (!modal.open) return; // closed while loading
    if (data.indexes.length === 0) {
      status.textContent = "No vector store, or it reports no statistics.";
      return;
    }
    const total = data.indexes.reduce((n, i) => n + i.count, 0);
    status.textContent = `${data.backend}: ${data.indexes.length} indexes, ${total} vectors`;

    const table = document.createElement("table");
    const head = table.createTHead().insertRow();
    for (const [label, num] of [
      ["Index", false], ["Vector dataset", false], ["Vectors", true], ["Rows", true], ["Missing", true],
      ["Dimensions", true], ["Embedding size", true],
    ] as [string, boolean][]) {
      const th = head.appendChild(document.createElement("th"));
      th.textContent = label;
      if (num) th.className = "num";
    }
    const body = table.createTBody();
    for (const ix of data.indexes) {
      const tr = body.insertRow();
      tr.insertCell().textContent = ix.index;
      tr.insertCell().appendChild(document.createElement("code")).textContent = ix.vector_dataset;
      const missing = Math.max(0, ix.expected - ix.count);
      const cells = [ix.count, ix.expected, missing, ix.dimensions, formatBytes(ix.bytes)];
      cells.forEach((v, i) => {
        const td = tr.insertCell();
        td.className = "num";
        td.textContent = String(v);
        if (i === 2 && missing > 0) td.classList.add("error");
      });
    }
    content.appendChild(table);
    const note = content.appendChild(document.createElement("p"));
    note.className = "muted";
    note.textContent =
      "Rows: entities / edges of the dataset, which should each have one vector. Missing: rows without a vector. " +
      "Embedding size: raw floats only (vectors × dimensions × 4 bytes).";
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load vector stats: ${err instanceof Error ? err.message : String(err)}`;
  }
}

export function initVectorsModal(): void {
  const modal = el<HTMLDialogElement>("vectors-modal");
  el("vectors-close").addEventListener("click", () => modal.close());
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
}
