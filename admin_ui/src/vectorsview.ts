// The "vectors" view: per database, the vector indexes of every memory dataset (not training datasets): how many vectors
// each holds, the backend store, the storage it occupies and how long ago the last vector was written. The fetching and
// formatting is shared with the vector DB modal of the "main" view (vectors.ts).

import { formatBytes } from "./format.js";
import { addCell, addHeader, fetchAllVectorStats, lastWriteText, storageText } from "./vectors.js";
import { onViewShown } from "./views.js";

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));

async function loadDatabase(name: string, box: HTMLElement): Promise<void> {
  const status = box.appendChild(document.createElement("p"));
  status.className = "muted";
  status.textContent = "Loading…";
  try {
    const datasets = await fetchAllVectorStats(name);
    if (datasets.length === 0) {
      status.textContent = "No datasets.";
      return;
    }
    let vectors = 0;
    let disk = 0;
    let estimated = false;
    const table = document.createElement("table");
    addHeader(table, [
      ["Dataset", false], ["Index", false], ["Backend", false], ["Vectors", true], ["Storage", true], ["Last write", true],
    ]);
    const body = table.createTBody();
    for (const ds of datasets) {
      if (ds.indexes.length === 0) {
        const tr = body.insertRow();
        addCell(tr, ds.dataset, { title: `user: ${ds.user_id}` });
        const td = addCell(tr, "no vector store, or it reports no statistics", { cls: "muted" });
        td.colSpan = 5;
        continue;
      }
      ds.indexes.forEach((ix, i) => {
        const tr = body.insertRow();
        addCell(tr, i === 0 ? ds.dataset : "", { title: `user: ${ds.user_id}` });
        addCell(tr, ix.index, { title: ix.vector_dataset });
        addCell(tr, ix.backend);
        addCell(tr, String(ix.count), { num: true });
        const st = storageText(ix);
        addCell(tr, st.text, { num: true, title: st.title });
        const lw = lastWriteText(ix);
        addCell(tr, lw.text, { num: true, title: lw.title });
        vectors += ix.count;
        if (ix.disk_bytes > 0) disk += ix.disk_bytes;
        else if (ix.count > 0) { disk += ix.bytes; estimated = true; }
      });
    }
    status.textContent =
      `${datasets.length} dataset${datasets.length === 1 ? "" : "s"}, ${vectors} vectors, ` +
      `${estimated ? "~" : ""}${formatBytes(disk)}. ` +
      "Last write: chromem's is the modification time of its files; Pinecone does not record it (n/a). " +
      "~: estimated from the raw embeddings.";
    box.appendChild(table);
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load vector stats: ${errText(err)}`;
  }
}

async function load(): Promise<void> {
  const content = el("vectors-view-content");
  content.replaceChildren();
  let names: string[];
  try {
    const res = await fetch("api/databases");
    if (!res.ok) throw new Error(String(res.status));
    names = ((await res.json()) as { databases: string[] }).databases;
  } catch (err) {
    content.textContent = `Failed to list databases: ${errText(err)}`;
    return;
  }
  await Promise.all(names.map((name) => {
    const box = content.appendChild(document.createElement("div"));
    box.className = "mr-db";
    box.appendChild(document.createElement("h3")).textContent = `${name} database`;
    return loadDatabase(name, box);
  }));
}

export function initVectorsView(): void {
  el("vectors-view-refresh").addEventListener("click", () => void load());
  onViewShown("vectors", () => void load()); // the numbers change as datasets are written to
}
