// Admin UI extensions (docs/admin-extensions.md): code that a program embedding gligabao registers (gligabao.ServeOptions.AdminExtensions)
// to add views, and buttons and blocks inside the existing views. Each extension is an ES module served by the server; its default
// export is called with the `gb` object made here. Keep the types in gligabao-admin.d.ts (next to this directory) in step with this file.

import { alertDialog, confirmDialog } from "./dialogs.js";
import { addViewElements, onViewShown } from "./views.js";

// The version of the API of `gb`; it changes only when an extension can break. Same number as admin.ExtensionAPIVersion in Go.
const API_VERSION = 1;

type SlotFn = (container: HTMLElement, ctx: Record<string, unknown>) => void;

const slots = new Map<string, Array<{ ext: string; fn: SlotFn }>>();
const failed: string[] = [];

const CLOSE_ICON = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>';
const DEFAULT_ICON = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 4a2 2 0 1 1 4 0v2h4a1 1 0 0 1 1 1v4h-2a2 2 0 1 0 0 4h2v4a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1v-4h2a2 2 0 1 0 0-4H5V7a1 1 0 0 1 1-1h4V4Z"/></svg>';

function report(ext: string, what: string, err: unknown): void {
  const msg = `extension ${ext}: ${what}: ${err instanceof Error ? err.message : String(err)}`;
  failed.push(msg);
  console.error(msg, err);
}

// Fills the slot called name in container: every extension that registered for it adds what it wants. ctx is what the slot
// tells about where it is (e.g. the model of a menu). One extension failing does not stop the others.
export function renderSlot(name: string, container: HTMLElement, ctx: Record<string, unknown> = {}): void {
  for (const { ext, fn } of slots.get(name) ?? []) {
    try {
      fn(container, ctx);
    } catch (err) {
      report(ext, `slot ${name}`, err);
    }
  }
}

interface ViewSpec {
  id: string;
  title: string;
  icon?: string;
  tooltip?: string;
  render: (el: HTMLElement) => void | Promise<void>;
  onShow?: (el: HTMLElement) => void;
}

interface ModalSpec {
  title: string;
  body?: string | Node;
  footer?: Node | Node[];
  wide?: boolean;
  onClose?: () => void;
}

const LOAD_TIMEOUT_MS = 10000;
const ID_RE = /^[a-z][a-z0-9_-]*$/;

function makeGB(ext: string) {
  const base = `ext/${ext}/api/`; // relative to the admin UI's URL (/v1/gb/admin/)
  const gb = {
    apiVersion: API_VERSION,
    extension: ext,

    // A new view with a button in the left nav. render(el) is called the first time it is shown, onShow(el) every time it is shown.
    addView(spec: ViewSpec): HTMLElement {
      if (!ID_RE.test(spec.id)) throw new Error(`view id "${spec.id}" must be lowercase letters, digits, '_' or '-'`);
      const name = `${ext}/${spec.id}`;
      const el = document.createElement("div");
      el.id = `view-ext-${ext}-${spec.id}`;
      el.className = "view ext-view";
      el.dataset.view = name;
      el.hidden = true;
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "view-btn ext-view-btn";
      btn.dataset.view = name;
      btn.setAttribute("aria-pressed", "false");
      btn.title = spec.tooltip ?? spec.title;
      btn.innerHTML = spec.icon ?? DEFAULT_ICON; // the icon is the extension's own markup
      const label = document.createElement("span");
      label.textContent = spec.title;
      btn.append(label);
      addViewElements(name, btn, el);
      let rendered = false;
      onViewShown(name, () => {
        if (!rendered) {
          rendered = true;
          try {
            const r = spec.render(el);
            if (r) r.catch((err) => report(ext, `view ${spec.id}`, err));
          } catch (err) {
            report(ext, `view ${spec.id}`, err);
          }
        }
        try {
          spec.onShow?.(el);
        } catch (err) {
          report(ext, `view ${spec.id}`, err);
        }
      });
      return el;
    },

    // Adds UI to an existing place of the admin UI (see docs/admin-extensions.md for the list): fn(container, ctx) is called
    // each time the place is drawn. Register in the default function, before it returns.
    slot(name: string, fn: SlotFn): void {
      slots.set(name, [...(slots.get(name) ?? []), { ext, fn }]);
    },

    // fetch for the extension's own backend (ServeOptions.AdminExtensions[].Handler): path is relative to its api/ root.
    api(path: string, init?: RequestInit): Promise<Response> {
      return fetch(base + path.replace(/^\/+/, ""), init);
    },

    // Like api, for a JSON answer: parses it and throws an Error (with the server's message) if the status is not 2xx.
    // A `body` that is not a string/FormData/Blob is sent as JSON.
    async json<T = unknown>(path: string, init: RequestInit & { body?: unknown } = {}): Promise<T> {
      const { body, ...rest } = init;
      const opts: RequestInit = { ...rest };
      if (body !== undefined) {
        const raw = typeof body === "string" || body instanceof FormData || body instanceof Blob;
        opts.body = raw ? (body as BodyInit) : JSON.stringify(body);
        if (!raw) opts.headers = { "Content-Type": "application/json", ...(rest.headers as Record<string, string> | undefined) };
      }
      const res = await gb.api(path, opts);
      const text = await res.text();
      if (!res.ok) throw new Error(text.trim() || `${res.status} ${res.statusText}`);
      return (text ? JSON.parse(text) : null) as T;
    },

    // A button in the style of the admin UI.
    button(label: string, onClick: (e: MouseEvent) => void, opts: { title?: string; small?: boolean; danger?: boolean } = {}): HTMLButtonElement {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = label;
      if (opts.title) b.title = opts.title;
      if (opts.small) b.classList.add("small");
      if (opts.danger) b.classList.add("danger");
      b.addEventListener("click", onClick);
      return b;
    },

    // A dialog (a string body is shown as text; pass a Node for markup). Returns it with its body element and close().
    modal(spec: ModalSpec) {
      const dlg = document.createElement("dialog");
      dlg.className = "modal" + (spec.wide ? "" : " modal-small");
      const head = document.createElement("div");
      head.className = "modal-head";
      const h2 = document.createElement("h2");
      h2.textContent = spec.title;
      const x = document.createElement("button");
      x.type = "button";
      x.className = "close-x";
      x.title = "Close";
      x.setAttribute("aria-label", "Close");
      x.innerHTML = CLOSE_ICON;
      x.addEventListener("click", () => dlg.close());
      head.append(h2, x);
      const body = document.createElement("div");
      body.className = "modal-body";
      if (spec.body !== undefined) body.append(spec.body);
      dlg.append(head, body);
      if (spec.footer) {
        const foot = document.createElement("div");
        foot.className = "modal-foot";
        foot.append(...(Array.isArray(spec.footer) ? spec.footer : [spec.footer]));
        dlg.append(foot);
      }
      dlg.addEventListener("close", () => {
        dlg.remove();
        spec.onClose?.();
      });
      document.body.append(dlg);
      dlg.showModal();
      return { el: dlg, body, setTitle: (t: string) => void (h2.textContent = t), close: () => dlg.close() };
    },

    // The admin UI's own confirm / alert dialogs.
    confirm: (message: string, opts?: { title?: string; okLabel?: string; danger?: boolean }): Promise<boolean> => confirmDialog(message, opts),
    alert: (message: string, opts?: { title?: string }): Promise<void> => alertDialog(message, opts),

    // Escapes text for use in innerHTML.
    escape: (s: unknown): string => String(s).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`),
  };
  return gb;
}

// Places an empty block at the top and the bottom of every built-in view, and one in the top bar, for the slots "<view>.top",
// "<view>.bottom" and "topbar".
function drawStaticSlots(): void {
  const block = (tag: string, name: string): HTMLElement => {
    const e = document.createElement(tag);
    e.className = "ext-slot";
    e.dataset.slot = name;
    renderSlot(name, e);
    return e;
  };
  document.querySelector(".health-badges")?.prepend(block("span", "topbar"));
  for (const v of document.querySelectorAll<HTMLElement>(".view:not(.ext-view)")) {
    const name = v.dataset.view ?? "";
    v.prepend(block("div", `${name}.top`));
    v.append(block("div", `${name}.bottom`));
  }
}

interface Listing {
  api_version: number;
  extensions: Array<{ name: string; script: string; style?: string }>;
}

// Loads the extensions the server lists and runs them. Call it before the views are initialised, so that their views and slots
// are there when the admin UI draws. A broken extension is logged to the console and shown in the footer; the rest still load.
export async function loadExtensions(): Promise<void> {
  let list: Listing;
  try {
    const res = await fetch("api/extensions");
    if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
    list = (await res.json()) as Listing;
  } catch (err) {
    report("(list)", "could not list the extensions", err);
    drawStaticSlots();
    showFailures();
    return;
  }
  const loadOne = async (e: Listing["extensions"][number]): Promise<void> => {
    try {
      if (e.style) {
        const link = document.createElement("link");
        link.rel = "stylesheet";
        link.href = e.style;
        document.head.append(link);
      }
      const mod = (await import(new URL(e.script, document.baseURI).href)) as { default?: (gb: ReturnType<typeof makeGB>) => unknown };
      if (typeof mod.default !== "function") throw new Error("index.js has no default export function");
      await mod.default(makeGB(e.name));
    } catch (err) {
      report(e.name, "failed to load", err);
    }
  };
  // An extension that never finishes loading must not keep the admin UI from starting.
  const timeout = (e: { name: string }): Promise<void> =>
    new Promise((resolve) => setTimeout(() => { report(e.name, "failed to load", new Error(`not loaded after ${LOAD_TIMEOUT_MS / 1000}s`)); resolve(); }, LOAD_TIMEOUT_MS));
  await Promise.all(list.extensions.map((e) => Promise.race([loadOne(e), timeout(e)])));
  drawStaticSlots();
  showFailures();
}

// Shows what failed to load above the footer.
function showFailures(): void {
  if (failed.length === 0) return;
  const p = document.createElement("p");
  p.id = "ext-errors";
  p.className = "error ext-errors";
  p.textContent = failed.join(" | ");
  document.getElementById("version")?.before(p);
}
