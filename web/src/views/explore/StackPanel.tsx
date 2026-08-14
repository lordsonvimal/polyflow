// UN.4 — the tech-stack view (problem 9: "what is this fleet built with?").
// Canvas-free: a plain scrolling page of per-service cards plus a workspace
// header row, entirely off data /api/stack (per-service) and /api/graph
// (cross-service edges, reusing lib/aggregate.ts's existing service-level
// aggregation rather than a new server call).
import { For, Show, createEffect, createMemo, createResource, onCleanup, onMount } from "solid-js";
import { treeStore, type ServiceSummary, type DependencyInfo } from "../../stores/tree";
import { paletteStore } from "../../stores/palette";
import { scopeStore } from "../../stores/scope";
import { exploreStore } from "../../stores/explore";
import { fetchAllGraph } from "../canvas/scopes/common";
import { aggregateServices } from "../../lib/aggregate";
import { edgeGroupOf } from "../../lib/edgeGroups";
import type { GraphNode, GraphEdge } from "../../lib/types";
import { ListSkeleton } from "../../shell/Skeleton";
import EmptyState from "../../shell/EmptyState";

const DEPS_VISIBLE_CAP = 12;

// Pure — total node/edge counts and file count across every service, for
// the workspace header row.
export function computeTotals(services: ServiceSummary[]): { services: number; files: number; nodes: number; edges: number } {
  let files = 0, nodes = 0, edges = 0;
  for (const s of services) {
    files += s.files;
    for (const n of Object.values(s.nodeCounts)) nodes += n;
    for (const n of Object.values(s.edgeCounts)) edges += n;
  }
  return { services: services.length, files, nodes, edges };
}

// Pure — cross-service edges (any pair of differently-served nodes),
// summed per coarse edge-type group (lib/edgeGroups.ts, the same grouping
// FilterBar/lenses use), for the "http ×12 · rabbitmq ×2"-style summary
// line UN.1's overview scope already renders on canvas.
export function crossServiceChannelCounts(
  nodes: GraphNode[],
  edges: GraphEdge[],
): { group: string; count: number }[] {
  const agg = aggregateServices(nodes, edges);
  const byGroup = new Map<string, number>();
  for (const e of agg.edges) {
    const group = edgeGroupOf(e.type);
    // aggregateServices' label is "type" (count 1) or "type ×N" — the count
    // it already computed, parsed back out rather than re-walking raw edges.
    const m = /×(\d+)$/.exec(e.label ?? "");
    const n = m ? parseInt(m[1], 10) : 1;
    byGroup.set(group, (byGroup.get(group) ?? 0) + n);
  }
  return [...byGroup.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([group, count]) => ({ group, count }));
}

// Sorted desc by count, ties broken alphabetically (rule 2, deterministic).
function sortedCounts(counts: Record<string, number>): [string, number][] {
  return Object.entries(counts).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

function BarList(props: { title: string; counts: Record<string, number>; onClick?: (kind: string) => void }) {
  const rows = createMemo(() => sortedCounts(props.counts));
  const max = createMemo(() => rows().reduce((m, [, n]) => Math.max(m, n), 0) || 1);
  return (
    <div>
      <div class="text-[10px] uppercase tracking-wide text-neutral-400 mb-1">{props.title}</div>
      <Show when={rows().length > 0} fallback={<div class="text-xs text-neutral-500">none</div>}>
        <div class="flex flex-col gap-0.5">
          <For each={rows()}>
            {([kind, count]) => (
              <button
                class="flex items-center gap-2 text-xs group text-left disabled:cursor-default"
                disabled={!props.onClick}
                onClick={() => props.onClick?.(kind)}
              >
                <span class="w-32 shrink-0 truncate text-neutral-400 group-hover:text-neutral-200" title={kind}>{kind}</span>
                <span class="flex-1 min-w-[24px] h-2 bg-neutral-800 rounded-sm overflow-hidden">
                  <span class="block h-full bg-indigo-600/70" style={{ width: `${(count / max()) * 100}%` }} />
                </span>
                <span
                  data-testid={`stack-count-${props.title}-${kind}`}
                  class={`w-10 text-right shrink-0 ${props.onClick ? "text-indigo-300 group-hover:text-indigo-200" : "text-neutral-400"}`}
                >
                  {count}
                </span>
              </button>
            )}
          </For>
        </div>
      </Show>
    </div>
  );
}

function ServiceCard(props: { svc: ServiceSummary; focused: boolean }) {
  let ref: HTMLDivElement | undefined;
  createEffect(() => {
    if (props.focused) ref?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  });

  const openDeps = () =>
    scopeStore.push({ kind: "service", service: props.svc.name });
  const openNodeKind = (kind: string) =>
    paletteStore.openWithQuery(`kind:${kind} service:${props.svc.name}`);

  return (
    <div
      ref={ref}
      data-testid={`stack-card-${props.svc.name}`}
      class={`p-3 rounded border ${props.focused ? "border-indigo-500" : "border-neutral-800"} bg-neutral-900`}
    >
      <div class="flex items-baseline justify-between gap-2 mb-2">
        <span class="font-medium text-white">{props.svc.name}</span>
        <button
          data-testid={`stack-files-${props.svc.name}`}
          class="text-xs text-indigo-300 hover:text-indigo-200"
          title="Open service scope"
          onClick={openDeps}
        >
          {props.svc.files} files
        </button>
      </div>
      <div class="text-xs text-neutral-400 mb-2">
        {props.svc.language || "(unknown language)"}
        <Show when={props.svc.frameworks.length > 0}>
          {" · "}{props.svc.frameworks.join(", ")}
        </Show>
      </div>
      <div class="mb-2">
        <div class="text-[10px] uppercase tracking-wide text-neutral-400 mb-1">Dependencies</div>
        <Show
          when={props.svc.deps.length > 0}
          fallback={<div class="text-xs text-neutral-500">no dependency manifest found</div>}
        >
          <ul class="text-xs text-neutral-400 flex flex-col gap-0.5">
            <For each={props.svc.deps.slice(0, DEPS_VISIBLE_CAP)}>
              {(d: DependencyInfo) => (
                <li class="flex justify-between gap-2">
                  <span class="truncate">{d.name}</span>
                  <span class="text-neutral-500 shrink-0">{d.version} · {d.ecosystem}</span>
                </li>
              )}
            </For>
          </ul>
          <Show when={props.svc.deps.length > DEPS_VISIBLE_CAP}>
            <div class="text-xs text-neutral-500 mt-0.5">+{props.svc.deps.length - DEPS_VISIBLE_CAP} more</div>
          </Show>
        </Show>
      </div>
      {/* Stacked, not side-by-side: at the panel's typical width a 2-column
          split left label/bar/count too little room and the count digits
          overlapped the next column's label. */}
      <div class="flex flex-col gap-3">
        <BarList title="Nodes" counts={props.svc.nodeCounts} onClick={openNodeKind} />
        {/* No per-edge-type search chip exists (only node `kind:` is
            filterable), so edge counts are shown without a click-navigate —
            an honest gap rather than a fabricated filter. */}
        <BarList title="Edges" counts={props.svc.edgeCounts} />
      </div>
    </div>
  );
}

export default function StackPanel() {
  onMount(() => treeStore.loadServices());
  const [crossGraph] = createResource(() => fetchAllGraph());

  const totals = createMemo(() => computeTotals(treeStore.services()));
  const channelSummary = createMemo(() => {
    const g = crossGraph();
    if (!g) return [];
    return crossServiceChannelCounts(g.nodes, g.edges);
  });

  createEffect(() => {
    const focused = exploreStore.focusService();
    if (!focused) return;
    // Read once, then release — a repeat navigation to the same service
    // should still re-trigger the scroll/highlight.
    const t = setTimeout(() => exploreStore.clearFocusService(), 1500);
    onCleanup(() => clearTimeout(t));
  });

  return (
    <div data-testid="stack-panel" class="p-3 flex flex-col gap-3 overflow-y-auto h-full text-sm text-neutral-300">
      <Show
        when={!treeStore.servicesError()}
        fallback={<EmptyState icon="⚠" message="Failed to load stack" detail={treeStore.servicesError()} />}
      >
      <Show when={!treeStore.servicesLoading()} fallback={<ListSkeleton />}>
        <Show
          when={treeStore.services().length > 0}
          fallback={<EmptyState message="No services indexed" detail="Run `polyflow index` to populate the stack view." />}
        >
          <div data-testid="stack-header" class="p-2 rounded border border-neutral-800 bg-neutral-900/60">
            <div class="text-xs text-neutral-400">
              {totals().services} services · {totals().files} files · {totals().nodes} nodes · {totals().edges} edges
            </div>
            <Show
              when={channelSummary().length > 0}
              fallback={<div class="text-xs text-neutral-500 mt-1">no cross-service edges found</div>}
            >
              <div class="text-xs text-neutral-400 mt-1">
                <For each={channelSummary()}>
                  {(c, i) => (
                    <>
                      <Show when={i() > 0}>{" · "}</Show>
                      <span>{c.group} ×{c.count}</span>
                    </>
                  )}
                </For>
              </div>
            </Show>
          </div>
          <div class="grid gap-3" style={{ "grid-template-columns": "repeat(auto-fill, minmax(260px, 1fr))" }}>
            <For each={treeStore.services()}>
              {(svc) => <ServiceCard svc={svc} focused={exploreStore.focusService() === svc.name} />}
            </For>
          </div>
        </Show>
      </Show>
      </Show>
    </div>
  );
}
