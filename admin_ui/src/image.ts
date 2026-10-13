// Remember an image: paste an image anywhere on the page, it opens as an overlay; "Remember" sends it to
// POST /v1/gb/remember and the extracted metadata is shown next to the image (with a spinner while waiting).

interface TextBlock { kind: string; text: string }
interface ImageResult {
  user_id: string;
  dataset: string;
  image_artifact_id: number;
  extraction_id: number;
  media_type: string;
  sha256: string;
  size_bytes: number;
  path: string;
  cached: boolean;
  valid_at: string;
  created_at: string;
  theme: string;
  title: string;
  text: string;
  text_blocks: TextBlock[] | null;
  provider?: string;
  model?: string;
  input_tokens?: number;
  output_tokens?: number;
  cost_usd?: number;
}

const MAX_IMAGE_BYTES = 5 << 20; // memory.MaxImageBytes
const SUPPORTED = ["image/jpeg", "image/png", "image/gif", "image/webp"];
const STORE_USER = "gligabao.admin.remember.user_id";
const STORE_DATASET = "gligabao.admin.remember.dataset";

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / 1024 / 1024).toFixed(2)} MiB`;
}

function readBase64(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onerror = () => reject(r.error ?? new Error("cannot read the image"));
    r.onload = () => resolve(String(r.result).replace(/^data:[^,]*,/, ""));
    r.readAsDataURL(file);
  });
}

let current: Blob | null = null;
let objectURL = "";
let abort: AbortController | null = null;

function closeOverlay(): void {
  abort?.abort();
  abort = null;
  current = null;
  if (objectURL) URL.revokeObjectURL(objectURL);
  objectURL = "";
  el("image-overlay").hidden = true;
  el<HTMLImageElement>("image-preview").removeAttribute("src");
}

function setBusy(busy: boolean): void {
  el("image-spinner").hidden = !busy;
  el<HTMLButtonElement>("image-submit").disabled = busy;
  el<HTMLInputElement>("image-user").disabled = busy;
  el<HTMLInputElement>("image-dataset").disabled = busy;
}

function setMessage(text: string, isError: boolean): void {
  const m = el("image-message");
  m.className = isError ? "error" : "muted";
  m.textContent = text;
}

function openOverlay(file: Blob): void {
  abort?.abort();
  current = file;
  if (objectURL) URL.revokeObjectURL(objectURL);
  objectURL = URL.createObjectURL(file);
  el<HTMLImageElement>("image-preview").src = objectURL;
  el("image-info").textContent = `${file.type} · ${formatBytes(file.size)}`;
  el("image-result").replaceChildren();
  setBusy(false);
  if (!SUPPORTED.includes(file.type)) setMessage(`Unsupported image type "${file.type}" (JPEG, PNG, GIF or WebP).`, true);
  else if (file.size > MAX_IMAGE_BYTES) setMessage(`Image is larger than ${formatBytes(MAX_IMAGE_BYTES)}.`, true);
  else setMessage("Pick the user and dataset, then press Remember.", false);
  el("image-overlay").hidden = false;
}

function row(dl: HTMLElement, key: string, value: string): void {
  if (!value) return;
  dl.appendChild(document.createElement("dt")).textContent = key;
  dl.appendChild(document.createElement("dd")).textContent = value;
}

// textContent only: never inject server data as HTML.
function renderResult(r: ImageResult): void {
  const box = el("image-result");
  const h = document.createElement("h3");
  h.textContent = r.title || "(no title)";
  const dl = document.createElement("dl");
  row(dl, "Theme", r.theme);
  row(dl, "Status", r.cached ? "cached (already in the dataset; nothing re-processed)" : "newly stored");
  row(dl, "User / dataset", `${r.user_id} / ${r.dataset}`);
  row(dl, "Image artifact", String(r.image_artifact_id));
  row(dl, "Extraction", String(r.extraction_id));
  row(dl, "Media type", r.media_type);
  row(dl, "Size", formatBytes(r.size_bytes));
  row(dl, "SHA-256", r.sha256);
  row(dl, "Path", r.path);
  row(dl, "Valid at", r.valid_at);
  row(dl, "Created at", r.created_at);
  row(dl, "Model", [r.provider, r.model].filter(Boolean).join(" / "));
  if (r.input_tokens !== undefined || r.output_tokens !== undefined) {
    row(dl, "Tokens", `${r.input_tokens ?? 0} in / ${r.output_tokens ?? 0} out`);
  }
  if (r.cost_usd !== undefined) row(dl, "Cost", `$${r.cost_usd.toFixed(5)}`);
  box.replaceChildren(h, dl);

  const blocks = r.text_blocks ?? [];
  const bh = document.createElement("h4");
  bh.textContent = `Text blocks (${blocks.length})`;
  box.appendChild(bh);
  for (const b of blocks) {
    const d = document.createElement("div");
    d.className = "image-block";
    const k = d.appendChild(document.createElement("span"));
    k.className = "image-block-kind";
    k.textContent = b.kind;
    d.appendChild(document.createElement("span")).textContent = b.text;
    box.appendChild(d);
  }
}

async function submit(): Promise<void> {
  if (!current) return;
  if (!SUPPORTED.includes(current.type) || current.size > MAX_IMAGE_BYTES) return;
  const user = el<HTMLInputElement>("image-user").value.trim();
  const dataset = el<HTMLInputElement>("image-dataset").value.trim();
  if (!user) {
    setMessage("user_id is required.", true);
    return;
  }
  localStorage.setItem(STORE_USER, user);
  localStorage.setItem(STORE_DATASET, dataset);

  const ctl = new AbortController();
  abort = ctl;
  el("image-result").replaceChildren();
  setMessage("", false);
  setBusy(true);
  try {
    const image = await readBase64(current);
    const body: Record<string, string> = { user_id: user, image };
    if (dataset) body.dataset = dataset;
    const res = await fetch("../remember", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal: ctl.signal,
    });
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    const data = (await res.json()) as ImageResult;
    renderResult(data);
    document.dispatchEvent(new CustomEvent("image-remembered")); // the image store card refreshes
    setMessage("", false);
  } catch (err) {
    if (ctl.signal.aborted) return;
    setMessage(`Remember failed: ${err instanceof Error ? err.message : String(err)}`, true);
  } finally {
    if (abort === ctl) {
      abort = null;
      setBusy(false);
    }
  }
}

export function initImageOverlay(): void {
  el<HTMLInputElement>("image-user").value = localStorage.getItem(STORE_USER) ?? "";
  el<HTMLInputElement>("image-dataset").value = localStorage.getItem(STORE_DATASET) ?? "";

  document.addEventListener("paste", (e) => {
    const items = e.clipboardData?.items;
    if (!items) return;
    for (const it of items) {
      if (it.kind === "file" && it.type.startsWith("image/")) {
        const file = it.getAsFile();
        if (file) {
          e.preventDefault();
          openOverlay(file);
          return;
        }
      }
    }
  });

  el<HTMLFormElement>("image-form").addEventListener("submit", (e) => {
    e.preventDefault();
    void submit();
  });
  el("image-close").addEventListener("click", closeOverlay);
  // Click on the tinted background (not on the panel) closes; so does Escape.
  el("image-overlay").addEventListener("mousedown", (e) => {
    if (e.target === e.currentTarget) closeOverlay();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !el("image-overlay").hidden) closeOverlay();
  });
}
