import { createSignal } from "solid-js";

export type ToastKind = "info" | "success" | "error";
export type Toast = {
  id: string;
  kind: ToastKind;
  message: string;
  // Verbatim server error body / stack, shown behind a details expander.
  detail?: string;
};

// info/success toasts self-dismiss; error toasts persist until the user
// dismisses them (US.5: "long operations handled with care").
const AUTO_DISMISS_MS = 5000;

const [toasts, setToasts] = createSignal<Toast[]>([]);
const timers = new Map<string, ReturnType<typeof setTimeout>>();
let seq = 0;

function clearTimer(id: string) {
  const t = timers.get(id);
  if (t !== undefined) {
    clearTimeout(t);
    timers.delete(id);
  }
}

function dismiss(id: string): void {
  clearTimer(id);
  setToasts((ts) => ts.filter((t) => t.id !== id));
}

function add(toast: Partial<Toast> & Pick<Toast, "kind" | "message">): string {
  const id = toast.id ?? `toast-${++seq}`;
  const full: Toast = { id, kind: toast.kind, message: toast.message, detail: toast.detail };
  setToasts((ts) => [...ts.filter((t) => t.id !== id), full]);
  if (full.kind !== "error") {
    clearTimer(id);
    timers.set(id, setTimeout(() => dismiss(id), AUTO_DISMISS_MS));
  }
  return id;
}

function clear(): void {
  timers.forEach((t) => clearTimeout(t));
  timers.clear();
  setToasts([]);
}

export const notificationsStore = {
  toasts,
  add,
  dismiss,
  clear,
};
