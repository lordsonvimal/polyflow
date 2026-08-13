import { Show, For } from "solid-js";
import { selectionStore } from "../stores/selection";
import type { Selection } from "../stores/selection";
import SourcePanel from "./SourcePanel";

function PinnedPanel({ sel }: { sel: NonNullable<Selection> }) {
  return (
    <div class="w-80 bg-neutral-950 border-l border-neutral-800 overflow-y-auto shrink-0">
      <div class="p-4 text-sm text-neutral-300">
        <div class="flex items-start justify-between mb-1 gap-2">
          <span class="font-medium text-blue-300 break-all min-w-0" title={sel.id}>📌 {sel.kind}: {sel.id}</span>
          <button
            class="text-xs text-neutral-500 hover:text-white shrink-0"
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
              <div class="flex items-start justify-between gap-2 mb-2">
                <span class="font-medium break-all min-w-0" title={sel().id}>{sel().kind}: {sel().id}</span>
                <div class="flex gap-2 shrink-0">
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
              <Show when={sel().kind === "node"}>
                <SourcePanel nodeId={sel().id} />
              </Show>
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
