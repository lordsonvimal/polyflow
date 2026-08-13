import { Show } from "solid-js";
import { selectionStore } from "../stores/selection";

export default function DetailHost() {
  return (
    <div
      data-testid="detail-host"
      class="shrink-0 border-l border-neutral-800 dark:border-neutral-700 bg-neutral-950 overflow-y-auto transition-all"
      style={{ width: selectionStore.selection() ? "320px" : "0px" }}
    >
      <Show when={selectionStore.selection()}>
        {(sel) => (
          <div class="p-4 text-sm text-neutral-300">
            <div class="font-medium mb-1">{sel().kind}: {sel().id}</div>
            <button
              class="text-xs text-neutral-500 hover:text-white"
              onClick={() => selectionStore.setSelection(null)}
            >
              × close
            </button>
          </div>
        )}
      </Show>
    </div>
  );
}
