import { Show, For, createEffect, createSignal, createMemo } from "solid-js";
import { selectionStore } from "../stores/selection";
import type { Selection } from "../stores/selection";
import { scopeStore } from "../stores/scope";
import GroupSummary from "../views/flows/GroupSummary";
import SourcePanel from "./SourcePanel";
import { isServiceNodeId, isContainerGroupNodeId, serviceFromNodeId } from "../lib/aggregate";
import { exploreStore } from "../stores/explore";
import { layoutPrefs } from "../stores/layoutPrefs";
import { importRollupStore } from "../stores/importRollup";
import { flowsThroughStore } from "../stores/flowsThrough";
import ThroughPanel from "../views/flows/ThroughPanel";
import { pathFinderStore } from "../stores/pathFinder";
import PathFinderPanel from "../views/flows/PathFinderPanel";
import { linkExplorerStore } from "../stores/linkExplorer";
import LinkExplorer from "../views/explore/LinkExplorer";
import { servicePairStore } from "../stores/servicePair";
import ServicePairPanel from "../views/flows/ServicePairPanel";
import SeamSummary from "../views/flows/SeamSummary";
import { contextCopyStore } from "../stores/contextCopy";
import { pinboardStore } from "../stores/pinboard";
import Resizer from "./Resizer";

function PinnedPanel({ sel }: { sel: NonNullable<Selection> }) {
  return (
    <div class="w-80 bg-neutral-950 border-l border-neutral-800 overflow-y-auto shrink-0">
      <div class="p-4 text-sm text-neutral-300">
        <div class="flex items-center justify-between gap-2 mb-1">
          <span class="text-xs uppercase tracking-wide text-neutral-400">📌 {sel.kind}</span>
          <button
            class="text-xs text-neutral-400 hover:text-white shrink-0"
            onClick={() => selectionStore.unpin(sel.id)}
          >
            × unpin
          </button>
        </div>
        <div class="font-medium text-blue-300 truncate" title={sel.id}>{sel.id}</div>
      </div>
    </div>
  );
}

export default function DetailHost() {
  const [flowsOpenFor, setFlowsOpenFor] = createSignal<string | null>(null);

  // UF.4: group scope has no `selection` — the group itself is the thing
  // being inspected, so its summary is keyed off the scope stack instead
  // of selectionStore, alongside (not replacing) the selection-driven panel
  // below.
  const groupScope = createMemo(() => {
    const top = scopeStore.stack().at(-1);
    return top?.kind === "group" ? top : null;
  });

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

  // UF.8: palette's "Explore links" result action — same bridge shape as
  // flowsThroughStore above.
  const [linksOpenFor, setLinksOpenFor] = createSignal<string | null>(null);
  createEffect(() => {
    const requested = linkExplorerStore.requestedNodeId();
    if (!requested) return;
    setLinksOpenFor(requested);
    linkExplorerStore.consume();
  });

  // UF.5: "Copy context" on the node/edge detail panel — only real graph
  // nodes/edges, never a synthetic overview-aggregation pill (`agg:`) or
  // Imports-lens rollup (`rollup:`) UB.6 has no node/edge for.
  const isCopyableSelection = (sel: NonNullable<Selection>) =>
    !sel.id.startsWith("agg:") && !sel.id.startsWith("rollup:") && !(sel.kind === "node" && isContainerGroupNodeId(sel.id));

  return (
    <div
      data-testid="detail-host"
      class="flex shrink-0 border-l border-neutral-800 dark:border-neutral-700 overflow-hidden"
    >
      <Show when={groupScope() || selectionStore.selection()}>
        <Resizer width={layoutPrefs.detailWidth} setWidth={layoutPrefs.setDetailWidth} invert />
      </Show>
      <Show when={groupScope()}>
        {(g) => (
          <div class="bg-neutral-950 overflow-y-auto shrink-0" style={{ width: `${layoutPrefs.detailWidth()}px` }}>
            <div class="p-4 text-sm text-neutral-300">
              <div class="flex items-start justify-between gap-2 mb-2">
                <span class="font-medium">{g().nodeIds.length} selected — Group</span>
              </div>
              <GroupSummary nodeIds={g().nodeIds} />
            </div>
          </div>
        )}
      </Show>
      <Show when={selectionStore.selection()}>
        {(sel) => (
          <div class="bg-neutral-950 overflow-y-auto shrink-0" style={{ width: `${layoutPrefs.detailWidth()}px` }}>
            <div class="p-4 text-sm text-neutral-300">
              <div class="flex items-center justify-between gap-2 mb-1">
                <span class="text-xs uppercase tracking-wide text-neutral-400">{sel().kind}</span>
                <button
                  class="text-xs text-neutral-400 hover:text-white shrink-0"
                  onClick={() => selectionStore.setSelection(null)}
                >
                  × close
                </button>
              </div>
              <div class="font-medium truncate mb-2" title={sel().id}>{sel().id}</div>
              <div class="flex flex-wrap gap-x-3 gap-y-1 mb-2">
                <Show when={isCopyableSelection(sel())}>
                  <button
                    data-testid="detail-copy-context"
                    class="text-xs text-blue-400 hover:text-blue-300"
                    title="Copy context"
                    onClick={() => contextCopyStore.copy({ kind: sel().kind, id: sel().id })}
                  >
                    ⧉ copy context
                  </button>
                </Show>
                <button
                  class="text-xs text-blue-400 hover:text-blue-300"
                  title="Pin to compare"
                  onClick={() => selectionStore.pin(sel())}
                >
                  📌 pin
                </button>
                {/* UF.7: pinboard chip — distinct feature from the
                    "📌 pin"/compare button above (selectionStore, capped
                    at 2, ephemeral). Only meaningful for a real node. */}
                <Show when={sel().kind === "node" && !isContainerGroupNodeId(sel().id)}>
                  <button
                    data-testid="detail-pin-to-pinboard"
                    class="text-xs text-blue-400 hover:text-blue-300"
                    title={pinboardStore.isPinned(sel().id) ? "Unpin from pinboard" : "Pin to pinboard"}
                    onClick={() => pinboardStore.toggle({ id: sel().id, label: sel().id })}
                  >
                    {pinboardStore.isPinned(sel().id) ? "📍 unpin" : "📍 pinboard"}
                  </button>
                </Show>
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
              <Show when={sel().kind === "node" && !isContainerGroupNodeId(sel().id)}>
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
                <button
                  data-testid="detail-explore-links"
                  class="text-xs text-indigo-300 hover:text-indigo-200 mt-2 block"
                  onClick={() => setLinksOpenFor(linksOpenFor() === sel().id ? null : sel().id)}
                >
                  {linksOpenFor() === sel().id ? "▾" : "▸"} Explore links
                </button>
                <Show when={linksOpenFor() === sel().id}>
                  <LinkExplorer nodeId={sel().id} />
                </Show>
              </Show>
              <Show when={sel().kind === "edge" && importRollupStore.get(sel().id)}>
                {(concrete) => (
                  <div data-testid="rollup-detail">
                    <div class="text-xs text-neutral-400 mb-1">
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
              {/* UF.3: aggregated overview service-pair pill → channel drill-in. */}
              <Show when={sel().kind === "edge" && servicePairStore.pair()?.edgeId === sel().id}>
                {() => <ServicePairPanel from={servicePairStore.pair()!.from} to={servicePairStore.pair()!.to} />}
              </Show>
              {/* UF.3: any other real edge → its seam summary. */}
              <Show
                when={
                  sel().kind === "edge" &&
                  !sel().id.startsWith("agg:") &&
                  !sel().id.startsWith("rollup:") &&
                  servicePairStore.pair()?.edgeId !== sel().id
                }
              >
                <SeamSummary edgeId={sel().id} />
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
