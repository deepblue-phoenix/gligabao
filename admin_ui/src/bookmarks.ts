// Bookmarks of the admin UI: things the operator wants to come back to (an eval case run, a remember run, a query run). They are kept
// in the browser (localStorage), not on the server, so they belong to this browser. Each view that has bookmarkable things draws a star
// button with bookmarkButton and registers, with registerBookmarkOpener, the function that opens a bookmark of its kind; the "bookmarks"
// view (initBookmarks) lists them all and calls those functions.

export type BookmarkKind = "eval_case" | "remember_run" | "query_run";

// What a view gives to bookmark something: id is unique per thing (the same thing is never bookmarked twice), title and detail are
// what the list shows, ref is whatever the opener needs to find the thing again.
export interface BookmarkSpec {
  id: string;
  kind: BookmarkKind;
  title: string;
  detail: string;
  ref: Record<string, string | number>;
}

export interface Bookmark extends BookmarkSpec {
  created_at: string; // ISO
}

const STORE = "gligabao.admin.bookmarks";
const CHANGED = "bookmarkschanged";

const KIND_LABELS: Record<BookmarkKind, string> = { eval_case: "eval case", remember_run: "remember run", query_run: "query run" };
const KIND_ORDER: BookmarkKind[] = ["eval_case", "remember_run", "query_run"];

const openers = new Map<BookmarkKind, (b: Bookmark) => Promise<void>>();

// Registers how a bookmark of this kind is opened (it should throw if the thing is gone).
export function registerBookmarkOpener(kind: BookmarkKind, open: (b: Bookmark) => Promise<void>): void {
  openers.set(kind, open);
}

export function loadBookmarks(): Bookmark[] {
  try {
    const v = JSON.parse(localStorage.getItem(STORE) ?? "[]") as unknown;
    return Array.isArray(v) ? (v as Bookmark[]).filter((b) => b && typeof b.id === "string" && b.kind in KIND_LABELS) : [];
  } catch {
    return [];
  }
}

function save(list: Bookmark[]): void {
  localStorage.setItem(STORE, JSON.stringify(list));
  window.dispatchEvent(new Event(CHANGED));
}

export const isBookmarked = (id: string): boolean => loadBookmarks().some((b) => b.id === id);

export function addBookmark(spec: BookmarkSpec): void {
  const list = loadBookmarks().filter((b) => b.id !== spec.id);
  list.push({ ...spec, created_at: new Date().toISOString() });
  save(list);
}

export function removeBookmark(id: string): void {
  save(loadBookmarks().filter((b) => b.id !== id));
}

// A button that bookmarks or un-bookmarks the thing, and shows which of the two it is (★ bookmarked, ☆ not). It follows the changes made
// anywhere else (another copy of the button, the bookmarks view).
export function bookmarkButton(spec: BookmarkSpec): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "small bookmark-btn";
  const paint = (): void => {
    const on = isBookmarked(spec.id);
    btn.textContent = on ? "★ Bookmarked" : "☆ Bookmark";
    btn.classList.toggle("on", on);
    btn.title = on ? "Remove this bookmark" : "Bookmark this to come back to it from the bookmarks view";
    btn.setAttribute("aria-pressed", String(on));
  };
  const onChange = (): void => {
    if (!btn.isConnected) { window.removeEventListener(CHANGED, onChange); return; } // the view drew itself again
    paint();
  };
  btn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    if (isBookmarked(spec.id)) {
      removeBookmark(spec.id);
      return;
    }
    addBookmark(spec);
    if (isBookmarked(spec.id)) { // it was saved (localStorage can be full or off): a little pop
      btn.classList.remove("pop");
      void btn.offsetWidth; // restart the animation if it is pressed again at once
      btn.classList.add("pop");
    }
  });
  btn.addEventListener("animationend", () => btn.classList.remove("pop"));
  window.addEventListener(CHANGED, onChange);
  paint();
  return btn;
}

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

function node<K extends keyof HTMLElementTagNameMap>(tag: K, cls = "", text = ""): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text) e.textContent = text; // textContent only: never inject data as HTML
  return e;
}

const fmtTime = (iso: string): string => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toISOString().replace("T", " ").slice(0, 19);
};

// "5 mins ago" / "3 hours ago" / "2 days ago" / "3 weeks ago" / "2 months ago" ("just now" under a minute; a month is 30 days).
function savedAgo(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const mins = Math.max(0, Math.floor((Date.now() - t) / 60000));
  const plural = (n: number, unit: string) => `${n} ${unit}${n === 1 ? "" : "s"} ago`;
  if (mins < 1) return "just now";
  if (mins < 60) return plural(mins, "min");
  if (mins < 1440) return plural(Math.floor(mins / 60), "hour");
  if (mins < 10080) return plural(Math.floor(mins / 1440), "day");
  if (mins < 43200) return plural(Math.floor(mins / 10080), "week");
  return plural(Math.floor(mins / 43200), "month");
}

const TRASH = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 6h18"/><path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/><path d="M10 11v6M14 11v6"/></svg>';

let filter: BookmarkKind | "" = "";

async function open(b: Bookmark, status: HTMLElement): Promise<void> {
  const opener = openers.get(b.kind);
  if (!opener) return;
  status.className = "muted";
  status.textContent = `Opening ${b.title}…`;
  try {
    await opener(b);
    status.textContent = "";
  } catch (err) {
    status.className = "error";
    status.textContent = `Could not open ${b.title}: ${err instanceof Error ? err.message : String(err)}. It may have been deleted; remove the bookmark if so.`;
  }
}

function render(): void {
  const all = loadBookmarks();
  const status = el("bookmarks-status");
  const bar = el("bookmarks-filter");
  const box = el("bookmarks-content");

  bar.replaceChildren();
  const chip = (label: string, value: BookmarkKind | "", n: number): void => {
    const b = bar.appendChild(node("button", `small${filter === value ? " selected" : ""}`, `${label} (${n})`));
    b.type = "button";
    b.addEventListener("click", () => { filter = value; render(); });
  };
  chip("all", "", all.length);
  for (const k of KIND_ORDER) chip(KIND_LABELS[k], k, all.filter((b) => b.kind === k).length);

  const shown = all.filter((b) => !filter || b.kind === filter).sort((a, b) => b.created_at.localeCompare(a.created_at));
  if (shown.length === 0) {
    box.replaceChildren(node("p", "muted", all.length === 0
      ? "No bookmarks yet. Use the ☆ Bookmark button on an eval case, a remember run or a query run."
      : "No bookmarks of this kind."));
    return;
  }
  const table = node("table", "clickable");
  const head = table.createTHead().insertRow();
  for (const label of ["Type", "Bookmark", "Details", "Saved", ""]) head.appendChild(node("th", "", label));
  const body = table.createTBody();
  for (const b of shown) {
    const tr = body.insertRow();
    tr.insertCell().appendChild(node("span", `badge bm-${b.kind}`, KIND_LABELS[b.kind]));
    const title = tr.insertCell();
    title.className = "bm-title";
    title.textContent = b.title;
    title.title = b.title; // the whole text, when it is cut
    const detail = tr.insertCell();
    detail.className = "muted";
    detail.textContent = b.detail;
    const saved = tr.insertCell();
    saved.textContent = savedAgo(b.created_at);
    saved.title = `${fmtTime(b.created_at)} UTC`;
    const actions = tr.insertCell();
    const rm = actions.appendChild(node("button", "icon danger", ""));
    rm.type = "button";
    rm.title = "Remove this bookmark";
    rm.setAttribute("aria-label", "Remove this bookmark");
    rm.innerHTML = TRASH; // a fixed icon, no data in it
    rm.addEventListener("click", (ev) => { ev.stopPropagation(); removeBookmark(b.id); });
    tr.addEventListener("click", () => void open(b, status));
  }
  box.replaceChildren(table);
}

export function initBookmarks(): void {
  window.addEventListener(CHANGED, render);
  window.addEventListener("storage", (e) => { if (e.key === STORE) render(); }); // changed in another tab
  render();
}
