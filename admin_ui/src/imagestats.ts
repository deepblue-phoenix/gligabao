// The "Image store" dashboard card: how many images the database tracks (by media type and theme), the LLM usage
// of their extractions, and how the image directory on disk compares with the database.

import { formatBytes } from "./format.js";

interface Group { name: string; count: number; bytes: number }
interface Disk { dir: string; exists: boolean; files: number; bytes: number; missing: number; untracked: number }
interface ImageStats {
  images: number;
  bytes: number;
  extractions: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  media_types: Group[];
  themes: Group[];
  disk?: Disk;
}

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

function stat(parent: HTMLElement, label: string, value: string, cls = ""): void {
  const div = parent.appendChild(document.createElement("div"));
  div.appendChild(document.createElement("dt")).textContent = label;
  const dd = div.appendChild(document.createElement("dd"));
  dd.textContent = value;
  if (cls) dd.className = cls;
}

// A small table of groups (media types, themes); textContent only, as server data is never HTML.
function groupTable(title: string, groups: Group[], withBytes: boolean): HTMLElement {
  const box = document.createElement("div");
  box.className = "image-stats-group";
  box.appendChild(document.createElement("h3")).textContent = title;
  if (groups.length === 0) {
    const none = box.appendChild(document.createElement("p"));
    none.className = "muted";
    none.textContent = "none";
    return box;
  }
  const table = box.appendChild(document.createElement("table"));
  table.className = "compact";
  for (const g of groups) {
    const tr = table.insertRow();
    tr.insertCell().textContent = g.name;
    const c = tr.insertCell();
    c.className = "num";
    c.textContent = String(g.count);
    if (withBytes) {
      const b = tr.insertCell();
      b.className = "num";
      b.textContent = formatBytes(g.bytes);
    }
  }
  return box;
}

async function load(): Promise<void> {
  const status = el("images-status");
  const stats = el("images-stats");
  const detail = el("images-detail");
  status.className = "muted";
  status.textContent = "Loading…";
  try {
    const res = await fetch("api/images");
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    const s = (await res.json()) as ImageStats;
    stats.replaceChildren();
    detail.replaceChildren();
    stat(stats, "Images", String(s.images));
    stat(stats, "Size", formatBytes(s.bytes));
    stat(stats, "Extractions", String(s.extractions));
    stat(stats, "Tokens in / out", `${s.input_tokens} / ${s.output_tokens}`);
    stat(stats, "LLM cost", `$${s.cost_usd.toFixed(4)}`);
    if (s.disk) {
      stat(stats, "Files on disk", `${s.disk.files} (${formatBytes(s.disk.bytes)})`);
      stat(stats, "Missing files", String(s.disk.missing), s.disk.missing > 0 ? "error" : "");
      stat(stats, "Untracked files", String(s.disk.untracked), s.disk.untracked > 0 ? "warn" : "");
    }
    detail.append(groupTable("Media types", s.media_types ?? [], true), groupTable("Themes", s.themes ?? [], false));
    status.textContent = s.disk
      ? `${s.disk.exists ? "Directory" : "Directory (does not exist yet)"}: ${s.disk.dir}`
      : "Image directory unknown.";
  } catch (err) {
    status.className = "error";
    status.textContent = `Failed to load image statistics: ${err instanceof Error ? err.message : String(err)}`;
  }
}

export function initImageStats(): void {
  el("images-refresh").addEventListener("click", () => void load());
  // A new image was remembered (see image.ts): the numbers changed.
  document.addEventListener("image-remembered", () => void load());
  void load();
}
