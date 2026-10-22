// The admin views and the buttons that switch between them (the nav on the left). The built-in views are in index.html; the ones
// added by extensions (extensions.ts) are appended with addViewElements. The chosen view is kept in the URL hash.

const shownHooks = new Map<string, Array<() => void>>();

const buttons = (): HTMLButtonElement[] => [...document.querySelectorAll<HTMLButtonElement>(".view-btn")];
const views = (): HTMLElement[] => [...document.querySelectorAll<HTMLElement>(".view")];

// Shows the view called name (the main one if there is none by that name) and marks its button.
export function showView(name: string): void {
  const all = views();
  if (!all.some((v) => v.dataset.view === name)) name = "main";
  for (const v of all) v.hidden = v.dataset.view !== name;
  for (const b of buttons()) {
    const on = b.dataset.view === name;
    b.classList.toggle("active", on);
    b.setAttribute("aria-pressed", String(on));
  }
  for (const fn of shownHooks.get(name) ?? []) fn();
}

// Calls fn every time the view called name is shown.
export function onViewShown(name: string, fn: () => void): void {
  shownHooks.set(name, [...(shownHooks.get(name) ?? []), fn]);
}

// Adds a view (its element, hidden) and the button that shows it. If the URL hash names it, it is shown at once.
export function addViewElements(name: string, button: HTMLButtonElement, view: HTMLElement): void {
  document.getElementById("views-nav")?.append(button);
  document.querySelector(".views")?.append(view);
  if (location.hash.slice(1) === name) showView(name);
}

// Wires the nav buttons (also the ones added later) and shows the view named in the URL hash.
export function initViews(): void {
  document.getElementById("views-nav")?.addEventListener("click", (e) => {
    const b = (e.target as Element).closest<HTMLButtonElement>(".view-btn");
    if (!b) return;
    const name = b.dataset.view ?? "main";
    showView(name);
    history.replaceState(null, "", name === "main" ? location.pathname + location.search : `#${name}`);
  });
  showView(location.hash.slice(1));
}
