// Vector index statistics of memory datasets: the fetching and formatting shared by the modal of one dataset (opened from the
// "main" view) and the "vectors" view (vectorsview.ts) that lists all datasets.

import { ageText, formatBytes } from "./format.js";

export interface IndexStats {
  index: string;
  vector_dataset: string;
  expected: number; // rows (entities / edges / chunks / summaries) that should have a vector
  backend: string;
  count: number;
  dimensions: number;
  bytes: number; // raw embeddings: count × dimensions × 4
  disk_bytes: number; // what the store occupies for the index; 0 if it cannot say
  last_write?: string; // ISO time of the last vector written; absent if the store does not record it, or the index is empty
}
export interface VectorStats { backend: string; indexes: IndexStats[] }
export interface DatasetVectors extends VectorStats { user_id: string; dataset: string }

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
  return (await res.json()) as T;
}

// The vector indexes of one live dataset.
export function fetchVectorStats(db: string, userID: string, dataset: string): Promise<VectorStats> {
  return getJSON<VectorStats>(`api/vectors?${new URLSearchParams({ db, user_id: userID, dataset })}`);
}

// The vector indexes of every live dataset of a database.
export async function fetchAllVectorStats(db: string): Promise<DatasetVectors[]> {
  const data = await getJSON<{ datasets: DatasetVectors[] | null }>(`api/vectors/all?${new URLSearchParams({ db })}`);
  return data.datasets ?? [];
}

// What an index occupies: the store's own size if it reports one, else the raw embeddings as an estimate ("~").
export function storageText(ix: IndexStats): { text: string; title: string } {
  if (ix.disk_bytes > 0) return { text: formatBytes(ix.disk_bytes), title: "Size of the index's files in the store." };
  if (ix.count === 0) return { text: formatBytes(0), title: "" };
  return {
    text: `~${formatBytes(ix.bytes)}`,
    title: "Estimate: the raw embeddings (vectors × dimensions × 4 bytes). The backend does not report its own size.",
  };
}

// How long ago the last vector was written. The vector stores keep no timestamps: chromem's is the modification time of
// its files, Pinecone has none ("n/a").
export function lastWriteText(ix: IndexStats): { text: string; title: string } {
  if (ix.last_write) return { text: ageText(ix.last_write), title: ix.last_write };
  if (ix.count === 0) return { text: "–", title: "No vectors." };
  return { text: "n/a", title: `The ${ix.backend} backend does not record when a vector was written.` };
}

// Adds a cell with textContent only (server data is never HTML).
export function addCell(tr: HTMLTableRowElement, text: string, opts: { num?: boolean; title?: string; cls?: string } = {}): HTMLTableCellElement {
  const td = tr.insertCell();
  td.textContent = text;
  if (opts.num) td.classList.add("num");
  if (opts.cls) td.classList.add(opts.cls);
  if (opts.title) td.title = opts.title;
  return td;
}

export function addHeader(table: HTMLTableElement, cols: [string, boolean][]): void {
  const head = table.createTHead().insertRow();
  for (const [label, num] of cols) {
    const th = head.appendChild(document.createElement("th"));
    th.textContent = label;
    if (num) th.className = "num";
  }
}

//---------------------------------------------------------------------------------------------
// The modal of one dataset.

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
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
    const data = await fetchVectorStats(db, userID, dataset);
    if (!modal.open) return; // closed while loading
    if (data.indexes.length === 0) {
      status.textContent = "No vector store, or it reports no statistics.";
      return;
    }
    const total = data.indexes.reduce((n, i) => n + i.count, 0);
    status.textContent = `${data.backend}: ${data.indexes.length} indexes, ${total} vectors`;

    const table = document.createElement("table");
    addHeader(table, [
      ["Index", false], ["Vector dataset", false], ["Vectors", true], ["Rows", true], ["Missing", true],
      ["Dimensions", true], ["Embedding size", true], ["Storage", true], ["Last write", true],
    ]);
    const body = table.createTBody();
    for (const ix of data.indexes) {
      const tr = body.insertRow();
      tr.insertCell().textContent = ix.index;
      tr.insertCell().appendChild(document.createElement("code")).textContent = ix.vector_dataset;
      const missing = Math.max(0, ix.expected - ix.count);
      addCell(tr, String(ix.count), { num: true });
      addCell(tr, String(ix.expected), { num: true });
      addCell(tr, String(missing), { num: true, cls: missing > 0 ? "error" : undefined });
      addCell(tr, String(ix.dimensions), { num: true });
      addCell(tr, formatBytes(ix.bytes), { num: true });
      const st = storageText(ix);
      addCell(tr, st.text, { num: true, title: st.title });
      const lw = lastWriteText(ix);
      addCell(tr, lw.text, { num: true, title: lw.title });
    }
    content.appendChild(table);
    const note = content.appendChild(document.createElement("p"));
    note.className = "muted";
    note.textContent =
      "Rows: entities / edges / chunks / summaries of the dataset, which should each have one vector. Missing: rows without a vector. " +
      "Embedding size: raw floats only (vectors × dimensions × 4 bytes). Storage: the index's files in the store, if it reports them.";
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
