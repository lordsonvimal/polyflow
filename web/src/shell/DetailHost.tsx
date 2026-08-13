import { Show, For } from "solid-js";
import { selectionStore } from "../stores/selection";
import type { Selection } from "../stores/selection";

function PinnedPanel({ sel }: { sel: NonNullable<Selection> }) {
  return (
    <div class="w-80 bg-neutral-950 border-l border-neutral-800 overflow-y-auto shrink-0">
      <div class="p-4 text-sm text-neutral-300">
        <div class="flex items-center justify-between mb-1">
          <span class="font-medium text-blue-300">📌 {sel.kind}: {sel.id}</span>
          <button
            class="text-xs text-neutral-500 hover:text-white"
            onClick={() => selectionStore.unpin(sel.id)}
          >
            × unpin
          </button>
        </div>
      </div>
    </div>
  );
}

export default function DetailHost() {
  return (
    <div
      data-testid="detail-host"
      class="flex shrink-0 border-l border-neutral-800 dark:border-neutral-700 overflow-hidden transition-all"
    >
      <Show when={selectionStore.selection()}>
        {(sel) => (
          <div class="w-80 bg-neutral-950 overflow-y-auto shrink-0">
            <div class="p-4 text-sm text-neutral-300">
              <div class="flex items-center justify-between mb-2">
                <span class="font-medium">{sel().kind}: {sel().id}</span>
                <div class="flex gap-2">
                  <button
                    class="text-xs text-blue-400 hover:text-blue-300"
                    title="Pin to compare"
                    onClick={() => selectionStore.pin(sel())}
                  >
                    📌 pin
                  </button>
                  <button
                    class="text-xs text-neutral-500 hover:text-white"
                    onClick={() => selectionStore.setSelection(null)}
                  >
                    × close
                  </button>
                </div>
              </div>
            </div>
          </div>
        )}
      </Show>
      <For each={selectionStore.pinned()}>
        {(pinned) => <PinnedPanel sel={pinned} />}
      </For>
    </div>
  );
}
