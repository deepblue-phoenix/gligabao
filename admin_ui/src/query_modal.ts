// The query button of the top bar and its modal (see docs/query.md#admin-ui): asks the knowledge base a question from any view. A big
// question box; pickers for the dataset (of the standard database, any user's), the model and the thinking level; the answer in the same
// modal, and the ID of the query run, a link that opens the details of that run (the modal of the "query" view, runs.ts).
// Calls POST api/query (the body of POST /v1/gb/query) and the lists of api/query-options and api/overview.

import { openQueryRun } from "./runs.js";

const DB = "standard"; // the database POST /v1/gb/query works on

interface Options { models: string[]; default_model: string; reasoning: string[] }
interface Dataset { user_id: string; name: string; deleted: boolean }
interface Answer {
  answer: string; query_run_id?: number; session_id?: string;
  evidence?: { entities?: unknown[]; edges?: unknown[]; passages?: unknown[] };
}

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));

// The error of a failed response: the "error" of its JSON body, else its text.
async function failure(res: Response): Promise<Error> {
  let text = (await res.text()).trim();
  try { text = (JSON.parse(text) as { error?: string }).error ?? text; } catch { /* not JSON: as it is */ }
  return new Error(`${res.status}: ${text}`);
}

let datasets: Dataset[] = [];
let options: Options | null = null;
let lastDataset = ""; // "user · name" of the dataset asked last

// Fills the pickers: the datasets every time the modal is opened (they change), the models and levels once.
async function loadPickers(): Promise<void> {
  const [ds, opts] = await Promise.all([
    fetch(`api/overview?${new URLSearchParams({ db: DB })}`).then(async (r) => {
      if (!r.ok) throw await failure(r);
      return ((await r.json()) as { datasets: Dataset[] | null }).datasets ?? [];
    }),
    options ? Promise.resolve(options) : fetch("api/query-options").then(async (r) => {
      if (!r.ok) throw await failure(r);
      return (await r.json()) as Options;
    }),
  ]);
  datasets = ds.filter((d) => !d.deleted);
  options = opts;

  const dataset = el<HTMLSelectElement>("query-dataset");
  dataset.replaceChildren(...datasets.map((d, i) => new Option(`${d.user_id} · ${d.name}`, String(i))));
  const same = datasets.findIndex((d) => `${d.user_id} · ${d.name}` === lastDataset);
  if (same >= 0) dataset.value = String(same); // the one asked last stays picked

  const model = el<HTMLSelectElement>("query-model");
  if (model.options.length === 0) {
    model.append(new Option(`default (${opts.default_model})`, ""));
    for (const m of opts.models) model.append(new Option(m, m));
    const thinking = el<HTMLSelectElement>("query-thinking");
    for (const l of opts.reasoning) thinking.append(new Option(l === "none" ? "none (default)" : l, l));
    thinking.value = "none";
  }
}

async function ask(): Promise<void> {
  const text = el<HTMLTextAreaElement>("query-text");
  const status = el("query-status");
  const submit = el<HTMLButtonElement>("query-submit");
  const result = el("query-result");
  const question = text.value.trim();
  const ds = datasets[Number(el<HTMLSelectElement>("query-dataset").value)];
  if (!question) {
    status.className = "error";
    status.textContent = "Write a question first.";
    text.focus();
    return;
  }
  if (!ds) {
    status.className = "error";
    status.textContent = "There is no dataset to ask.";
    return;
  }
  const model = el<HTMLSelectElement>("query-model").value;
  const body: Record<string, unknown> = { user_id: ds.user_id, dataset: ds.name, query: question, reasoning: el<HTMLSelectElement>("query-thinking").value };
  if (model) body.models = { query: model };
  lastDataset = `${ds.user_id} · ${ds.name}`;

  submit.disabled = true;
  result.hidden = true;
  status.className = "muted";
  status.textContent = "Asking…";
  try {
    const res = await fetch("api/query", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!res.ok) throw await failure(res);
    const a = (await res.json()) as Answer;
    status.textContent = "";
    el("query-answer").textContent = a.answer || "(no answer: the call only retrieved)";
    const ev = a.evidence ?? {};
    el("query-evidence").textContent = `evidence: ${(ev.entities ?? []).length} entities, ${(ev.edges ?? []).length} facts, ${(ev.passages ?? []).length} passages`;
    const link = el<HTMLButtonElement>("query-run-link");
    link.hidden = a.query_run_id == null;
    link.textContent = a.query_run_id == null ? "" : `#${a.query_run_id}`;
    link.dataset.run = String(a.query_run_id ?? "");
    result.hidden = false;
  } catch (err) {
    status.className = "error";
    status.textContent = `The query failed: ${errText(err)}`;
  } finally {
    submit.disabled = false;
  }
}

export function initQuery(): void {
  const modal = el<HTMLDialogElement>("query-modal");
  el("query-open").addEventListener("click", () => {
    el("query-status").className = "muted";
    el("query-status").textContent = "";
    modal.showModal();
    el("query-text").focus();
    loadPickers().catch((err: unknown) => {
      el("query-status").className = "error";
      el("query-status").textContent = `Failed to load the options: ${errText(err)}`;
    });
  });
  el("query-close").addEventListener("click", () => modal.close());
  modal.addEventListener("click", (e) => { if (e.target === modal) modal.close(); });
  el("query-submit").addEventListener("click", () => void ask());
  el("query-text").addEventListener("keydown", (e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) void ask(); });
  el("query-run-link").addEventListener("click", () => {
    const id = Number(el("query-run-link").dataset.run);
    if (!id) return;
    openQueryRun(DB, id).catch((err: unknown) => {
      el("query-status").className = "error";
      el("query-status").textContent = `Failed to open the run: ${errText(err)}`;
    });
  });
}
