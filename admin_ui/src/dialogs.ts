// In-page replacements for the browser's confirm()/alert(): both open the modal #confirm-modal (index.html), which sits on
// top of any other open dialog. confirmDialog resolves true on OK and false on Cancel/Esc; alertDialog resolves on close.

interface Options {
  title?: string;
  okLabel?: string;
  danger?: boolean;
}

function show(message: string, ask: boolean, opts: Options): Promise<boolean> {
  const modal = document.getElementById("confirm-modal") as HTMLDialogElement | null;
  if (!modal) return Promise.resolve(ask ? window.confirm(message) : (window.alert(message), true));
  const q = <T extends HTMLElement>(id: string) => modal.querySelector<T>(`#${id}`) as T;
  q("confirm-title").textContent = opts.title ?? (ask ? "Please confirm" : "Error");
  q("confirm-message").textContent = message;
  const ok = q<HTMLButtonElement>("confirm-ok");
  ok.textContent = opts.okLabel ?? (ask ? "Confirm" : "OK");
  ok.classList.toggle("danger", !!opts.danger);
  q<HTMLButtonElement>("confirm-cancel").hidden = !ask;

  return new Promise((resolve) => {
    const done = () => {
      ok.removeEventListener("click", onOK);
      q("confirm-cancel").removeEventListener("click", onCancel);
      modal.removeEventListener("close", done);
      resolve(modal.returnValue === "ok");
    };
    const onOK = () => { modal.returnValue = "ok"; modal.close("ok"); };
    const onCancel = () => { modal.returnValue = "cancel"; modal.close("cancel"); };
    ok.addEventListener("click", onOK);
    q("confirm-cancel").addEventListener("click", onCancel);
    modal.addEventListener("close", done);
    modal.returnValue = "cancel";
    modal.showModal();
    (ask && opts.danger ? q<HTMLButtonElement>("confirm-cancel") : ok).focus();
  });
}

export const confirmDialog = (message: string, opts: Options = {}): Promise<boolean> => show(message, true, opts);
export const alertDialog = (message: string, opts: Options = {}): Promise<void> => show(message, false, opts).then(() => undefined);
