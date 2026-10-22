// Types of the API that gligabao's admin UI gives to extensions (docs/admin-extensions.md). Optional: an extension is plain
// JavaScript. Use it for editor autocomplete, in index.js:
//
//   /** @param {import("./gligabao-admin").GB} gb */
//   export default function (gb) { ... }
//
// Keep it in step with admin_ui/src/extensions.ts (makeGB).

/** The context a slot is drawn with; what it holds is documented per slot. */
export type SlotContext = Record<string, unknown>;

/** Fills one place of the admin UI: add elements to `container`. Called each time the place is drawn. */
export type SlotFn = (container: HTMLElement, ctx: SlotContext) => void;

export interface ViewSpec {
  /** Lowercase letters, digits, `_` or `-`; unique within the extension. Part of the URL hash (`#<extension>/<id>`). */
  id: string;
  /** The text of the button in the left nav (the button wraps long text). */
  title: string;
  /** Markup of an `<svg>` for the button (16x16, `stroke="currentColor"`); a puzzle piece if omitted. */
  icon?: string;
  /** Tooltip of the button; the title if omitted. */
  tooltip?: string;
  /** Called the first time the view is shown: draw into `el`. May be async. */
  render: (el: HTMLElement) => void | Promise<void>;
  /** Called every time the view is shown, after `render` the first time (e.g. to reload data). */
  onShow?: (el: HTMLElement) => void;
}

export interface ModalSpec {
  title: string;
  /** A string is shown as text; pass a Node for markup. */
  body?: string | Node;
  /** Nodes (buttons) for the footer. */
  footer?: Node | Node[];
  /** A wide dialog; the default is a small one. */
  wide?: boolean;
  onClose?: () => void;
}

export interface Modal {
  el: HTMLDialogElement;
  body: HTMLElement;
  setTitle(title: string): void;
  close(): void;
}

export interface GB {
  /** Version of this API (1). Compare it if your extension needs a newer one. */
  readonly apiVersion: number;
  /** The name of the extension (the Name it was registered with). */
  readonly extension: string;

  /** Adds a view with a button in the left nav. Returns the view's element (the same as `render` gets). */
  addView(spec: ViewSpec): HTMLElement;
  /** Adds UI to an existing place of the admin UI. Call it in the default function, before it returns. */
  slot(name: string, fn: SlotFn): void;

  /** `fetch` for the extension's own backend (its Go Handler); `path` is relative to the Handler's root. */
  api(path: string, init?: RequestInit): Promise<Response>;
  /** Like `api` for JSON: parses the answer, throws an Error with the server's message if the status is not 2xx. A `body` that is not a string, FormData or Blob is sent as JSON. */
  json<T = unknown>(path: string, init?: Omit<RequestInit, "body"> & { body?: unknown }): Promise<T>;

  /** A button in the admin UI's style (not attached anywhere: append it). */
  button(label: string, onClick: (e: MouseEvent) => void, opts?: { title?: string; small?: boolean; danger?: boolean }): HTMLButtonElement;
  /** Opens a dialog. */
  modal(spec: ModalSpec): Modal;
  /** The admin UI's own confirm dialog: resolves true on OK. */
  confirm(message: string, opts?: { title?: string; okLabel?: string; danger?: boolean }): Promise<boolean>;
  /** The admin UI's own alert dialog. */
  alert(message: string, opts?: { title?: string }): Promise<void>;
  /** Escapes text for use in `innerHTML`. */
  escape(s: unknown): string;
}

/** What an extension's `index.js` exports as default. */
export type Extension = (gb: GB) => void | Promise<void>;
