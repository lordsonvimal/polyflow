import { For, Show, createSignal } from "solid-js";
import { notificationsStore, type Toast } from "../stores/notifications";

const KIND_STYLES: Record<Toast["kind"], string> = {
  info: "border-neutral-700 bg-neutral-800 text-neutral-200",
  success: "border-emerald-800 bg-emerald-900/60 text-emerald-200",
  error: "border-red-800 bg-red-900/60 text-red-200",
};

function ToastRow(props: { toast: Toast }) {
  const [expanded, setExpanded] = createSignal(false);
  return (
    <div
      data-testid="toast"
      data-kind={props.toast.kind}
      class={`flex flex-col gap-1 px-3 py-2 rounded border text-xs shadow-lg max-w-sm pointer-events-auto ${KIND_STYLES[props.toast.kind]}`}
    >
      <div class="flex items-start gap-2">
        <span class="flex-1 min-w-0 break-words">{props.toast.message}</span>
        <Show when={props.toast.action}>
          {(action) => (
            <button
              data-testid="toast-action"
              class="text-[10px] underline opacity-80 hover:opacity-100 shrink-0"
              onClick={() => action().onClick()}
            >
              {action().label}
            </button>
          )}
        </Show>
        <Show when={props.toast.detail}>
          <button
            data-testid="toast-expand"
            class="text-[10px] underline opacity-80 hover:opacity-100 shrink-0"
            onClick={() => setExpanded((v) => !v)}
          >
            {expanded() ? "hide" : "details"}
          </button>
        </Show>
        <button
          data-testid="toast-dismiss"
          class="opacity-60 hover:opacity-100 shrink-0"
          onClick={() => notificationsStore.dismiss(props.toast.id)}
        >
          ×
        </button>
      </div>
      <Show when={expanded() && props.toast.detail}>
        <pre data-testid="toast-detail" class="whitespace-pre-wrap break-words text-[10px] opacity-90 max-h-40 overflow-y-auto">
          {props.toast.detail}
        </pre>
      </Show>
    </div>
  );
}

export default function Toasts() {
  return (
    <div
      data-testid="toasts"
      class="fixed bottom-3 right-3 z-50 flex flex-col gap-2 pointer-events-none"
    >
      <For each={notificationsStore.toasts()}>{(t) => <ToastRow toast={t} />}</For>
    </div>
  );
}
