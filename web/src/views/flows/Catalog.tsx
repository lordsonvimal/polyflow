// UF.1: the "Flows activity" entrypoint catalog — the second catalog-style
// entry point into flows (alongside ThroughPanel's per-node "isolate flows
// through here"). Lists every /api/flows/entrypoints row; a row click
// isolates that entrypoint's own forward flow as a UF.0 lane.
import { createResource, createMemo, createSignal, For, Show, onMount, onCleanup } from "solid-js";
import { apiFetch } from "../../lib/apiFetch";
import { scopeStore } from "../../stores/scope";
import { formatLocation } from "../../lib/location";
import { computeWindow } from "../explore/virtualize";
import { registerCommand } from "../../commands/registry";
import { layoutPrefs } from "../../stores/layoutPrefs";

registerCommand({
  id: "flows:catalog",
  label: "Flows: Open catalog",
  run: () => layoutPrefs.setActivity("flows"),
});

interface EntrypointItem {
  nodeId: string;
  kind: string;
  label: string;
  service: string;
  file: string;
  line: number;
  endLine?: number;
  channel?: string;
}

interface SkippedCount {
  type: string;
  count: number;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function parseItem(raw: any): EntrypointItem {
  return {
    nodeId: raw.node_id,
    kind: raw.kind,
    label: raw.label,
    service: raw.service ?? "",
    file: raw.file ?? "",
    line: raw.line ?? 0,
    endLine: raw.end_line || undefined,
    channel: raw.channel || undefined,
  };
}

const KIND_ICON: Record<string, string> = {
  route: "→",
  http_handler: "→",
  worker: "⚙",
  subscriber: "◉",
  grpc_handler: "▣",
  graphql_resolver: "◈",
  function: "ƒ",
};

type SortKey = "kind" | "label" | "service";
const ROW_HEIGHT = 40;

// Pure — extracted so determinism (rule 2) is unit-testable without
// mounting the component: same input always yields the same order.
export function filterAndSort(
  items: EntrypointItem[],
  query: string,
  kindFilter: string | null,
  sortKey: SortKey,
): EntrypointItem[] {
  const needle = query.trim().toLowerCase();
  let out = items;
  if (kindFilter) out = out.filter((i) => i.kind === kindFilter);
  if (needle) {
    out = out.filter(
      (i) =>
        i.label.toLowerCase().includes(needle) ||
        i.service.toLowerCase().includes(needle) ||
        i.file.toLowerCase().includes(needle) ||
        (i.channel ?? "").toLowerCase().includes(needle),
    );
  }
  return [...out].sort((a, b) => {
    const cmp = a[sortKey].localeCompare(b[sortKey]);
    return cmp !== 0 ? cmp : a.nodeId.localeCompare(b.nodeId);
  });
}

export default function Catalog() {
  const [resolution] = createResource(async () => {
    const r = await apiFetch(`/api/flows/entrypoints`, { silent: true });
    const body = (await r.json()) as { entrypoints: unknown[]; skipped: SkippedCount[] };
    return {
      items: body.entrypoints.map(parseItem),
      skipped: body.skipped ?? [],
    };
  });

  const [query, setQuery] = createSignal("");
  const [kindFilter, setKindFilter] = createSignal<string | null>(null);
  const [sortKey, setSortKey] = createSignal<SortKey>("service");
  const [showSkippedDetail, setShowSkippedDetail] = createSignal(false);

  const kinds = createMemo(() => [...new Set((resolution()?.items ?? []).map((i) => i.kind))].sort());
  const rows = createMemo(() => filterAndSort(resolution()?.items ?? [], query(), kindFilter(), sortKey()));

  let scrollerRef: HTMLDivElement | undefined;
  const [scrollTop, setScrollTop] = createSignal(0);
  const [viewportHeight, setViewportHeight] = createSignal(480);
  onMount(() => {
    if (scrollerRef && typeof ResizeObserver !== "undefined") {
      const ro = new ResizeObserver(() => setViewportHeight(scrollerRef!.clientHeight || 480));
      ro.observe(scrollerRef);
      onCleanup(() => ro.disconnect());
    }
  });
  const win = createMemo(() => computeWindow(scrollTop(), viewportHeight(), ROW_HEIGHT, rows().length));
  const visibleRows = createMemo(() => rows().slice(win().start, win().end));

  const totalSkipped = createMemo(() => (resolution()?.skipped ?? []).reduce((n, s) => n + s.count, 0));

  function open(item: EntrypointItem) {
    scopeStore.push({ kind: "flow", flow: { kind: "through", nodeId: item.nodeId, entrypointId: item.nodeId } });
  }

  return (
    <div data-testid="flows-catalog" class="flex flex-col h-full min-h-0 text-xs">
      <div class="p-2 flex flex-col gap-2 shrink-0 border-b border-neutral-800">
        <input
          data-testid="catalog-search"
          class="w-full px-2 py-1 bg-neutral-900 border border-neutral-800 rounded text-neutral-100 outline-none"
          placeholder="Search entrypoints…"
          value={query()}
          onInput={(e) => setQuery(e.currentTarget.value)}
        />
        <div class="flex items-center gap-1 flex-wrap">
          <button
            class={`px-2 py-0.5 rounded border text-[11px] ${
              kindFilter() === null ? "bg-neutral-700 text-white border-neutral-600" : "text-neutral-500 border-neutral-800"
            }`}
            onClick={() => setKindFilter(null)}
          >
            all
          </button>
          <For each={kinds()}>
            {(k) => (
              <button
                data-testid={`catalog-kind-${k}`}
                class={`px-2 py-0.5 rounded border text-[11px] ${
                  kindFilter() === k ? "bg-neutral-700 text-white border-neutral-600" : "text-neutral-500 border-neutral-800"
                }`}
                onClick={() => setKindFilter(kindFilter() === k ? null : k)}
              >
                {KIND_ICON[k] ?? "•"} {k}
              </button>
            )}
          </For>
        </div>
        <div class="flex items-center gap-2 text-neutral-500">
          <span>Sort:</span>
          <For each={["kind", "label", "service"] as SortKey[]}>
            {(k) => (
              <button
                class={`px-1.5 py-0.5 rounded ${sortKey() === k ? "text-white" : "hover:text-neutral-300"}`}
                onClick={() => setSortKey(k)}
              >
                {k}
              </button>
            )}
          </For>
        </div>
      </div>

      <Show when={resolution.loading}>
        <div class="p-4 text-neutral-500">Loading entrypoints…</div>
      </Show>

      <Show when={!resolution.loading && rows().length === 0}>
        <div class="p-4 text-neutral-500" data-testid="catalog-empty">
          No entrypoints match.
        </div>
      </Show>

      <div ref={scrollerRef} class="flex-1 min-h-0 overflow-y-auto" onScroll={() => setScrollTop(scrollerRef?.scrollTop ?? 0)}>
        <div style={{ height: `${win().topPad}px` }} />
        <For each={visibleRows()}>
          {(item) => (
            <div
              data-testid="catalog-row"
              class="px-2 py-1.5 border-b border-neutral-900 hover:bg-neutral-800 cursor-pointer"
              style={{ height: `${ROW_HEIGHT}px` }}
              onClick={() => open(item)}
            >
              <div class="flex items-center gap-1.5">
                <span>{KIND_ICON[item.kind] ?? "•"}</span>
                <span class="text-neutral-200 truncate">{item.label}</span>
                <Show when={item.channel}>
                  <span class="text-neutral-500 ml-1 truncate">{item.channel}</span>
                </Show>
              </div>
              <div class="text-neutral-600 truncate">
                {item.service} · {formatLocation(item.file, item.line, item.endLine)}
              </div>
            </div>
          )}
        </For>
        <div style={{ height: `${win().bottomPad}px` }} />
      </div>

      <Show when={totalSkipped() > 0}>
        <div class="p-2 border-t border-neutral-800 text-neutral-500 shrink-0">
          <button data-testid="catalog-skipped-toggle" class="hover:text-neutral-300" onClick={() => setShowSkippedDetail((v) => !v)}>
            {totalSkipped()} not listed — show anyway
          </button>
          <Show when={showSkippedDetail()}>
            <ul class="mt-1" data-testid="catalog-skipped-detail">
              <For each={resolution()?.skipped ?? []}>
                {(s) => <li>{s.count} {s.type}</li>}
              </For>
            </ul>
          </Show>
        </div>
      </Show>
    </div>
  );
}
