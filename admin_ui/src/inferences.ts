// The "inferences" view (see docs/inferences.md): per database, the totals of the recorded model invocations (tokens,
// cost), broken down by operation, stage, model and thinking level, and the list of the inferences, newest first. Clicking a
// row of a breakdown filters the list by it.

interface Sums { inferences: number; input_tokens: number; output_tokens: number; cost_usd: number }
interface Breakdown extends Sums { key: string; label?: string }
interface Inference {
  id: number; type: string; stage?: string; model_id?: string; model?: string; provider?: string;
  reasoning: string; input_tokens: number; output_tokens: number; cost_usd: number; created_at: string;
}
interface Response {
  totals: Sums; by_type: Breakdown[]; by_stage: Breakdown[]; by_model: Breakdown[]; by_reasoning: Breakdown[];
  inferences: Inference[]; total: number; limit: number; offset: number;
}
type Dim = "type" | "stage" | "model_id" | "reasoning";
type Filter = Partial<Record<Dim, string>>;

const PAGE = 100;

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing element #${id}`);
  return e as T;
}

function node<K extends keyof HTMLElementTagNameMap>(tag: K, cls = "", text = ""): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text) e.textContent = text; // textContent only: never inject server data as HTML
  return e;
}

const fmtInt = (n: number): string => n.toLocaleString("en-US");
const fmtCost = (usd: number): string => `$${usd.toFixed(4)}`;
const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toISOString().replace("T", " ").slice(0, 19);
}

function tile(label: string, value: string, title = ""): HTMLElement {
  const d = node("div");
  if (title) d.title = title;
  d.append(node("dt", "", label), node("dd", "", value));
  return d;
}

function numCell(tr: HTMLTableRowElement, text: string): void {
  tr.insertCell().textContent = text;
  tr.cells[tr.cells.length - 1].className = "num";
}

function headRow(table: HTMLTableElement, cols: [string, boolean][]): void {
  const head = table.createTHead().insertRow();
  for (const [label, num] of cols) {
    const th = node("th", num ? "num" : "", label);
    head.appendChild(th);
  }
}

interface Panel {
  filter: Filter;
  offset: number;
  status: HTMLElement;
  stats: HTMLElement;
  breakdowns: HTMLElement;
  filterBar: HTMLElement;
  table: HTMLTableElement;
  body: HTMLTableSectionElement;
  pager: HTMLElement;
}

const panels = new Map<string, Panel>();

function makePanel(name: string): { box: HTMLElement; panel: Panel } {
  const box = node("div", "mr-db");
  box.appendChild(node("h3", "", `${name} database`));
  const stats = box.appendChild(node("dl", "stats"));
  const status = box.appendChild(node("p", "muted"));
  const breakdowns = box.appendChild(node("div", "row mr-breakdowns"));
  const filterBar = box.appendChild(node("div", "row mr-filter"));
  const table = box.appendChild(node("table"));
  table.hidden = true;
  headRow(table, [["ID", true], ["Time (UTC)", false], ["Type", false], ["Stage", false], ["Model", false], ["Thinking", false],
    ["Input tokens", true], ["Output tokens", true], ["Cost", true]]);
  const body = table.createTBody();
  const pager = box.appendChild(node("div", "row mr-pager"));
  return { box, panel: { filter: {}, offset: 0, status, stats, breakdowns, filterBar, table, body, pager } };
}

function breakdownTable(title: string, dim: Dim, rows: Breakdown[], p: Panel, name: string): HTMLElement {
  const wrap = node("div", "mr-breakdown");
  wrap.appendChild(node("h4", "", title));
  const table = wrap.appendChild(node("table", "clickable compact"));
  headRow(table, [[title, false], ["Inferences", true], ["Input", true], ["Output", true], ["Cost", true]]);
  const body = table.createTBody();
  for (const b of rows) {
    const tr = body.insertRow();
    const label = b.label || b.key || "(none)";
    tr.insertCell().textContent = label;
    numCell(tr, fmtInt(b.inferences));
    numCell(tr, fmtInt(b.input_tokens));
    numCell(tr, fmtInt(b.output_tokens));
    numCell(tr, fmtCost(b.cost_usd));
    if (!b.key) continue; // inferences without a value cannot be filtered for
    if (p.filter[dim] === b.key) tr.classList.add("selected");
    tr.title = `Show only inferences with this ${title.toLowerCase()}`;
    tr.addEventListener("click", () => {
      if (p.filter[dim] === b.key) delete p.filter[dim];
      else p.filter[dim] = b.key;
      p.offset = 0;
      void loadDatabase(name);
    });
  }
  return wrap;
}

function render(name: string, p: Panel, data: Response): void {
  const t = data.totals;
  p.stats.replaceChildren(
    tile("Inferences", fmtInt(t.inferences)), tile("Input tokens", fmtInt(t.input_tokens)),
    tile("Output tokens", fmtInt(t.output_tokens), "Thinking tokens included"), tile("Cost", fmtCost(t.cost_usd)),
  );
  p.breakdowns.replaceChildren(
    breakdownTable("Type", "type", data.by_type, p, name), breakdownTable("Stage", "stage", data.by_stage, p, name),
    breakdownTable("Model", "model_id", data.by_model, p, name), breakdownTable("Thinking", "reasoning", data.by_reasoning, p, name),
  );

  const labels: Record<Dim, string> = { type: "type", stage: "stage", model_id: "model", reasoning: "thinking" };
  const active = (Object.keys(p.filter) as Dim[]).filter((d) => p.filter[d]);
  p.filterBar.replaceChildren();
  if (active.length > 0) {
    const modelName = (id: string): string => data.by_model.find((m) => m.key === id)?.label || id;
    const text = active.map((d) => `${labels[d]}: ${d === "model_id" ? modelName(p.filter[d] ?? "") : p.filter[d]}`).join(", ");
    p.filterBar.appendChild(node("span", "muted", `Filtered by ${text}`));
    const clear = p.filterBar.appendChild(node("button", "small", "Clear filter"));
    clear.type = "button";
    clear.addEventListener("click", () => {
      p.filter = {};
      p.offset = 0;
      void loadDatabase(name);
    });
  }

  p.body.replaceChildren();
  for (const r of data.inferences) {
    const tr = p.body.insertRow();
    numCell(tr, String(r.id));
    tr.insertCell().textContent = fmtTime(r.created_at);
    tr.insertCell().textContent = r.type;
    tr.insertCell().textContent = r.stage ?? "";
    const model = tr.insertCell();
    model.textContent = r.model ?? "";
    if (r.provider) model.title = r.provider;
    tr.insertCell().textContent = r.reasoning;
    numCell(tr, fmtInt(r.input_tokens));
    numCell(tr, fmtInt(r.output_tokens));
    numCell(tr, fmtCost(r.cost_usd));
  }
  p.table.hidden = data.inferences.length === 0;
  p.status.className = "muted";
  p.status.textContent = data.total === 0 ? "No inferences." :
    `Showing ${data.offset + 1}–${data.offset + data.inferences.length} of ${fmtInt(data.total)} inferences.`;

  p.pager.replaceChildren();
  const page = (label: string, offset: number, enabled: boolean): void => {
    const b = p.pager.appendChild(node("button", "small", label));
    b.type = "button";
    b.disabled = !enabled;
    b.addEventListener("click", () => {
      p.offset = offset;
      void loadDatabase(name);
    });
  };
  if (data.total > data.limit) {
    page("← Newer", Math.max(0, data.offset - data.limit), data.offset > 0);
    page("Older →", data.offset + data.limit, data.offset + data.limit < data.total);
  }
}

async function loadDatabase(name: string): Promise<void> {
  const p = panels.get(name);
  if (!p) return;
  p.status.className = "muted";
  p.status.textContent = "Loading…";
  const params = new URLSearchParams({ db: name, limit: String(PAGE), offset: String(p.offset) });
  for (const [k, v] of Object.entries(p.filter)) if (v) params.set(k, v);
  try {
    const res = await fetch(`api/inferences?${params}`);
    if (!res.ok) throw new Error(`${res.status}: ${(await res.text()).trim()}`);
    render(name, p, (await res.json()) as Response);
  } catch (err) {
    p.status.className = "error";
    p.status.textContent = `Failed to load inferences: ${errText(err)}`;
  }
}

async function load(): Promise<void> {
  const root = el("inferences-content");
  if (panels.size === 0) {
    try {
      const res = await fetch("api/databases");
      if (!res.ok) throw new Error(`${res.status}`);
      const names = ((await res.json()) as { databases: string[] }).databases;
      for (const name of names) {
        const { box, panel } = makePanel(name);
        panels.set(name, panel);
        root.appendChild(box);
      }
    } catch (err) {
      root.textContent = `Failed to list databases: ${errText(err)}`;
      return;
    }
  }
  await Promise.all([...panels.keys()].map((n) => loadDatabase(n)));
}

export function initInferences(): void {
  el("inferences-refresh").addEventListener("click", () => void load());
  void load();
}
