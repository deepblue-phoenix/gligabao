# Admin UI extensions

A program that embeds gligabao (see [Embedding in Go programs](../README.md#embedding-in-go-programs)) can extend the [admin UI](../README.md#admin-ui) with

- **new views**, each with a button in the nav on the left, and
- **buttons, modals and blocks inside the existing views** (for example an extra action in a trained model's menu).

An extension is a folder of plain JavaScript (and optionally CSS), plus optionally a Go HTTP handler that is its own backend. There is **no build step and no framework**, and nothing in gligabao has to be changed or rebuilt: the embedding program hands the folder to `gligabao.Serve`, the server serves it, and the admin page loads it when it starts.

- [Quick start](#quick-start)
- [Registering an extension (Go)](#registering-an-extension-go)
- [The JavaScript API: `gb`](#the-javascript-api-gb)
- [Views](#views)
- [Slots: adding to existing views](#slots-adding-to-existing-views)
- [A backend for the extension](#a-backend-for-the-extension)
- [Looks](#looks)
- [How it works](#how-it-works)
- [Developing an extension](#developing-an-extension)
- [Limits and caveats](#limits-and-caveats)

## Quick start

Three files. In your Go program, a folder `admin_ext/hello/` with `index.js`:

```js
// admin_ext/hello/index.js
export default function (gb) {
  // a new view, and its button in the left nav
  gb.addView({
    id: "main",
    title: "hello",
    render(el) {
      el.innerHTML = `<section class="card"><h2>Hello</h2><p class="muted">From an extension.</p></section>`;
    },
  });

  // a button in the box that opens when a trained model is clicked
  gb.slot("training.model.actions", (container, ctx) => {
    container.append(gb.button("Hello", () => gb.alert(`Model #${ctx.model.id}`), { small: true }));
  });
}
```

and a Go file that embeds the folder and registers it:

```go
//go:embed admin_ext/hello
var helloFS embed.FS

func helloAssets() fs.FS {
    sub, _ := fs.Sub(helloFS, "admin_ext/hello") // rooted at the extension: index.js is at its top
    return sub
}

err := gligabao.Serve(ctx, cfg, gligabao.ServeOptions{
    AdminExtensions: []gligabao.AdminExtension{{Name: "hello", Assets: helloAssets()}},
})
```

Start the server with `admin_ui = true` (or `--admin-ui`) and open `/v1/gb/admin/`: a **hello** button is in the nav.

## Registering an extension (Go)

Extensions are passed in `ServeOptions.AdminExtensions` (there is no command-line or configuration-file way to register one, since an extension is code, like the [hooks](hooks.md)):

```go
type AdminExtension struct {
    Name    string       // "billing": lowercase letters, digits, '_', '-', starting with a letter, at most 40
    Assets  fs.FS        // the browser files, rooted at the extension; index.js is required
    Handler http.Handler // optional: the extension's own HTTP API
}
```

- **`Name`** identifies the extension and is part of its URLs. It must be unique.
- **`Assets`** is any `fs.FS`: an `embed.FS` narrowed with `fs.Sub` (as above), or `os.DirFS("admin_ext/hello")` while developing. It must contain `index.js`; it may contain `style.css` (loaded with the extension) and anything else `index.js` imports.
- **`Handler`** is described in [A backend for the extension](#a-backend-for-the-extension).

They are validated at startup: `Serve` returns an error if a name is malformed or duplicated, `Assets` is missing or has no `index.js`. At most 50 extensions can be registered. Extensions are served only when the admin UI is enabled; without it `Serve` logs a warning for each and nothing is mounted.

## The JavaScript API: `gb`

`index.js` is an ES module. Its **default export** is a function that gets the `gb` object; it may be `async`. Everything the extension does starts from there:

| Call | What it does |
|---|---|
| `gb.addView({id, title, render, icon?, tooltip?, onShow?})` | Adds a [view](#views) with a nav button. |
| `gb.slot(name, fn)` | Adds UI to an existing place of the admin UI: [slots](#slots-adding-to-existing-views). |
| `gb.api(path, init?)` | `fetch` for the extension's own [backend](#a-backend-for-the-extension); returns the `Response`. |
| `gb.json(path, init?)` | Like `api`, for JSON: returns the parsed answer, and throws an `Error` with the server's message if the status is not 2xx. A `body` that is an object is sent as JSON. |
| `gb.button(label, onClick, {title?, small?, danger?}?)` | Returns a `<button>` in the admin UI's style (append it where you want it). |
| `gb.modal({title, body?, footer?, wide?, onClose?})` | Opens a dialog; returns `{el, body, setTitle(t), close()}`. A string `body` is shown as text; pass a DOM node for markup. Small by default, `wide: true` for a large one. |
| `gb.confirm(message, {title?, okLabel?, danger?}?)` | The admin UI's confirm dialog; a promise of `true` (OK) or `false`. |
| `gb.alert(message, {title?}?)` | The admin UI's alert dialog. |
| `gb.escape(text)` | Escapes text for use in `innerHTML`. |
| `gb.apiVersion` | The version of this API, `1` (see [versioning](#limits-and-caveats)). |
| `gb.extension` | The extension's `Name`. |

The extension can also call gligabao's own API with `fetch`, using absolute paths: `fetch("/v1/gb/training/models")`, `fetch("/v1/gb/admin/api/overview")`, and so on. The admin UI is served from the same origin, so no setup is needed.

Editor autocomplete is optional: [`admin_ui/gligabao-admin.d.ts`](../admin_ui/gligabao-admin.d.ts) has the types of everything above. In `index.js`:

```js
/** @param {import("./gligabao-admin").GB} gb */
export default function (gb) { /* ... */ }
```

(copy the `.d.ts` next to it, or point the import at it).

## Views

```js
gb.addView({
  id: "usage",            // lowercase letters, digits, '_', '-'; unique within the extension
  title: "usage",         // the text of the nav button
  icon: "<svg ...>",      // optional: 16x16 svg markup with stroke="currentColor"; a puzzle piece by default
  tooltip: "Usage stats", // optional: the button's hover text; default: the title
  render(el) { ... },     // called once, the first time the view is shown; may be async
  onShow(el) { ... },     // optional: called every time the view is shown (after render the first time)
});
```

- `el` is the view's empty `<div>`: draw into it. Wrap content in `<section class="card">` to look like the built-in views (see [Looks](#looks)).
- `render` runs **lazily**, on the first click on the button, so an extension that is never opened costs nothing but loading its script. Use `onShow` to reload data each time the user comes back.
- The view is addressed by `#<extension>/<id>` in the URL, like the built-in ones (`#training`), so a reload keeps it open.
- The buttons are stacked in the nav in the order the extensions finished loading, after the built-in ones. A long `title` wraps inside the button.
- An error thrown by `render` or `onShow` (or a rejected promise from `render`) is logged to the browser console and listed above the footer of the page; it does not break the rest of the admin UI.

## Slots: adding to existing views

A **slot** is a named place in the admin UI that extensions can add to. `gb.slot(name, fn)` registers `fn(container, ctx)`; the admin UI calls it **each time it draws that place**, with the element to add to and a context object that says where it is. Register slots inside the default function, before it returns (extensions load before the admin UI draws its views; a slot registered later is ignored by the places that were drawn already).

```js
gb.slot("main.bottom", (container) => {
  const card = document.createElement("section");
  card.className = "card";
  card.innerHTML = "<h2>My block</h2>";
  container.append(card);
});
```

The slots, v1:

| Slot | Where | `container` | `ctx` |
|---|---|---|---|
| `training.model.actions` | the little box of actions that opens when a row of the **trained models** table (training view) is clicked, after Info, Prompt, Eval and Audit | the box (a flex row of `button.small`s); add `gb.button(..., {small: true})`. The box closes after any button in it is clicked. | `{model}`: the trained model as the API returns it (`GET /v1/gb/training/models`): `id`, `name`, `job_id`, `model_size`, `dataset`, `num_train_steps`, `final_loss`, `files`, ... |
| `<view>.top` | the top of a built-in view | an empty `div` | `{}` |
| `<view>.bottom` | the bottom of a built-in view | an empty `div` | `{}` |
| `topbar` | next to the health badges in the top bar | a `span` (a flex row) | `{}` |

`<view>` is one of `main`, `evals`, `timeseries`, `inferences`, `remember_runs`, `query_runs`, `training` (the names of the nav buttons' `data-view`): `main.top`, `training.bottom`, and so on. Slots of the `<view>.top` / `.bottom` / `topbar` kind are drawn **once**, when the page starts. A slot nothing was added to is empty and invisible.

A slot function that throws is logged to the console and skipped; the other contributors still run.

Slots are the contract between gligabao and extensions: an extension can only add where gligabao marks a place. Missing a place you need (say, a button next to a dataset)? That is a one-line addition in `admin_ui/src`: call `renderSlot("<name>", element, { ...context })` (from `extensions.ts`) where the thing is drawn, and add a row to the table above.

## A backend for the extension

An extension that needs its own data or actions sets `Handler`, an ordinary `http.Handler`:

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /usage", func(w http.ResponseWriter, r *http.Request) {
    json.NewEncoder(w).Encode(map[string]any{"calls": 42})
})
mux.HandleFunc("POST /sync", syncHandler)

gligabao.AdminExtension{Name: "billing", Assets: assets, Handler: mux}
```

It is mounted at `/v1/gb/admin/ext/billing/api/` **with that prefix stripped**: the handler above sees `/usage`. Any method is passed through. The extension's JavaScript calls it with `gb.api` / `gb.json`:

```js
const usage = await gb.json("usage");                              // GET .../api/usage
await gb.json("sync", { method: "POST", body: { full: true } });  // POST with a JSON body
```

The handler can use anything in the embedding program (its own clients, databases, secrets). It may be called concurrently. It is protected by whatever protects the admin UI itself (see [caveats](#limits-and-caveats)).

## Looks

The extension runs in the admin page, so everything the admin UI's stylesheet defines is available. Use the same classes as the built-in views and the extension looks native:

| Class | Use |
|---|---|
| `card` | a panel with a border (put an `<h2>` in it) |
| `row` | a horizontal flex row with gaps, wraps |
| `row between` | same, with the first and last child pushed apart (the usual card heading with a button) |
| `muted`, `error` | grey and red text |
| `stats` | a row of `<dt>` (label) and `<dd>` (big number) pairs, in a `<dl>` |
| `badge badge-ok` / `badge-error` / `badge-unknown` / `badge-active` | small pills |
| `table-scroll` | wrap a `<table>` that may be wide; tables are styled by default, `class="clickable"` makes rows hover, `th.num` / `td.num` right-align numbers |
| `small` | on a `<button>`: the compact size; `danger` makes it red |
| `modal`, `modal-small`, `modal-head`, `modal-body`, `modal-foot` | the dialog structure (`gb.modal` builds it for you) |

Inputs, selects and buttons are styled by element. For anything else, put a `style.css` in the extension (loaded with it) and use the palette as CSS variables: `--gb-bg`, `--gb-surface`, `--gb-border`, `--gb-text`, `--gb-muted`, `--gb-accent`, `--gb-ok`, `--gb-err`. **Prefix your own class names** (`billing-...`): all extensions and the admin UI share one page.

## How it works

1. `Serve` validates the extensions and, if the admin UI is on, mounts them next to it:
   - `GET /v1/gb/admin/api/extensions` lists them: `{"api_version": 1, "extensions": [{"name": "billing", "script": "ext/billing/index.js", "style": "ext/billing/style.css"}]}` (`style` only if the extension has a `style.css`).
   - `GET /v1/gb/admin/ext/<name>/<file>` serves the extension's `Assets` (with `Cache-Control: no-cache`, so a changed extension is picked up by a reload; a request that is not `GET` there answers 405).
   - `/v1/gb/admin/ext/<name>/api/...` goes to the extension's `Handler` (any method), if it has one; 404 otherwise.
2. When the admin page starts it fetches the list, adds each `style`, `import()`s each `script` and calls its default export with `gb`. This happens before the admin UI draws anything, so the extensions' views and slots are there from the first paint. Extensions load in parallel; one that fails (a syntax error, a missing default export, a throw) or takes longer than 10 s is reported and skipped, the others are unaffected.
3. Whatever failed to load is logged to the browser console and shown in red above the page footer.

The URLs are relative to the admin UI's base (`/v1/gb/admin/`), and `gb.api` hides the prefix, so an extension never has to know them. An extension's own files can import each other with relative paths (`import { chart } from "./lib/chart.js"`); to use a third-party library, put its browser (ES module) build in the extension's folder and import it the same way. The admin UI already loads D3 as the global `d3`.

## Developing an extension

During development serve the folder from disk instead of embedding it, and a browser reload is enough to see a change in `index.js` or `style.css`, without rebuilding the Go program:

```go
Assets: os.DirFS("admin_ext/hello"), // instead of the embedded copy
```

Errors show up in the browser's console and above the page footer. `GET /v1/gb/admin/api/extensions` shows what the server registered.

## Limits and caveats

- **An extension is trusted code.** It runs in the admin page with the full rights of whoever uses it: it can call every endpoint of the server. Register only code you wrote or reviewed.
- **No access control of its own.** The extension's files and `Handler` are served on the same terms as the admin UI. gligabao has no authentication, so protect the admin UI (a reverse proxy, a private network), and with it the extensions. A `Handler` that does something dangerous should not rely on the extension's JavaScript being the only caller.
- **Versioning.** `gb.apiVersion` and `api_version` of the listing are `1`. The API changes in a way that can break extensions only with a new version number, and CHANGELOG.md says so; a new slot or a new `gb` function is not a break. An extension that needs something recent can check `gb.apiVersion` and report it with `gb.alert`.
- **Slot context is data, not an interface.** Treat `ctx` as read-only; do not keep elements you were given beyond the next redraw (the box of a slot is rebuilt each time it is drawn).
- **No hot add.** Extensions are registered at startup and loaded when the page loads; to add or remove one, restart the server and reload the page.
- At most 50 extensions; a name is at most 40 characters.
