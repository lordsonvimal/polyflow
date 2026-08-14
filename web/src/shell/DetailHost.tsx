import { Show, For, createEffect, createSignal } from "solid-js";
import { selectionStore } from "../stores/selection";
import type { Selection } from "../stores/selection";
import SourcePanel from "./SourcePanel";
import { isServiceNodeId, serviceFromNodeId } from "../lib/aggregate";
import { exploreStore } from "../stores/explore";
import { layoutPrefs } from "../stores/layoutPrefs";
import { importRollupStore } from "../stores/importRollup";
import { flowsThroughStore } from "../stores/flowsThrough";
import ThroughPanel from "../views/flows/ThroughPanel";
import { pathFinderStore } from "../stores/pathFinder";
import PathFinderPanel from "../views/flows/PathFinderPanel";

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
  const [flowsOpenFor, setFlowsOpenFor] = createSignal<string | null>(null);

  // A context-menu "Isolate flows through here" click selects the node and
  // leaves a one-shot request (flowsThroughStore) — auto-expand this
  // panel's own toggle for it, so the two entry points land in the same
  // place instead of drifting apart.
  createEffect(() => {
    const requested = flowsThroughStore.requestedNodeId();
    if (!requested) return;
    setFlowsOpenFor(requested);
    flowsThroughStore.consume();
  });

  // UF.2: "Find paths from A" on a second node — the context menu selects
  // that node and leaves a one-shot request here, same bridge shape as
  // flowsThroughStore above.
  const [pathsOpenFor, setPathsOpenFor] = createSignal<string | null>(null);
  createEffect(() => {
    const requested = pathFinderStore.requestedTo();
    if (!requested) return;
    setPathsOpenFor(requested.id);
    pathFinderStore.consume();
  });

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
              <Show when={sel().kind === "node" && isServiceNodeId(sel().id)}>
                <button
                  data-testid="detail-view-in-stack"
                  class="text-xs text-indigo-300 hover:text-indigo-200 mb-2"
                  onClick={() => {
                    layoutPrefs.setActivity("explore");
                    if (layoutPrefs.panelCollapsed()) layoutPrefs.setPanelCollapsed(false);
                    exploreStore.openStackFor(serviceFromNodeId(sel().id));
                  }}
                >
                  View in Tech Stack →
                </button>
              </Show>
              <Show when={sel().kind === "node" && !isServiceNodeId(sel().id)}>
                <SourcePanel nodeId={sel().id} />
                <button
                  data-testid="detail-isolate-flows-through"
                  class="text-xs text-indigo-300 hover:text-indigo-200 mt-2"
                  onClick={() => setFlowsOpenFor(flowsOpenFor() === sel().id ? null : sel().id)}
                >
                  {flowsOpenFor() === sel().id ? "▾" : "▸"} Isolate flows through here
                </button>
                <Show when={flowsOpenFor() === sel().id}>
                  <ThroughPanel nodeId={sel().id} />
                </Show>
                <Show when={pathsOpenFor() === sel().id && pathFinderStore.startNode()}>
                  {(start) => <PathFinderPanel from={start().id} fromLabel={start().label} to={sel().id} />}
                </Show>
              </Show>
              <Show when={sel().kind === "edge" && importRollupStore.get(sel().id)}>
                {(concrete) => (
                  <div data-testid="rollup-detail">
                    <div class="text-xs text-neutral-500 mb-1">
                      {concrete().length} concrete import{concrete().length === 1 ? "" : "s"}
                    </div>
                    <ul class="space-y-1">
                      <For each={concrete()}>
                        {(e) => (
                          <li class="text-xs text-neutral-300 break-all">
                            {e.from} → {e.to}
                          </li>
                        )}
                      </For>
                    </ul>
                  </div>
                )}
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
