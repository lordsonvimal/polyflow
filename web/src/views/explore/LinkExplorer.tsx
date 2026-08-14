// UF.8: link explorer — detailed upstream/downstream adjacency for the
// selected node, with a peek-vs-commit split per plan-10's binding
// principle: hover previews (flowHighlightStore, ViewState untouched),
// `＋` commit-expands (adds the row's node+edge to the canvas, budget-
// checked), `→` commit-navigates (pushes the row's file scope, UN.2
// behavior, same three-step sequence as the palette's openSymbol).
import { createResource, createSignal, createMemo, For, Show } from "solid-js";
import { apiFetch } from "../../lib/apiFetch";
import { scopeStore } from "../../stores/scope";
import { selectionStore } from "../../stores/selection";
import { treeStore } from "../../stores/tree";
import { flowHighlightStore } from "../../stores/flowHighlight";
import { canvasElementsStore } from "../../stores/canvasElements";
import { expandedElementsStore } from "../../stores/expandedElements";
import { parseQuery } from "../palette/query";
import { BUDGET } from "../canvas/budget";
import type { GraphNode, GraphEdge } from "../../lib/types";
import { displayLabel } from "../../lib/location";

export type LinkDirection = "upstream" | "downstream";

export interface LinkRow {
  node_id: string;
  label: string;
  type: string;
  service: string;
  file: string;
  line: number;
  edge_id: string;
  edge_type: string;
  edge_label?: string;
  channel?: string;
  cross_service?: boolean;
  confidence?: string;
  verification_state?: string;
  depth: number;
  via?: string[];
}

interface LinkExplorerResponse {
  direction: LinkDirection;
  depth: number;
  total: number;
  offset: number;
  rows: LinkRow[];
  truncated: boolean;
}

const VERIFICATION_DOT: Record<string, { title: string; class: string }> = {
  verified: { title: "verified", class: "bg-emerald-400" },
  candidate: { title: "candidate", class: "bg-amber-400" },
  conflicting: { title: "conflicting", class: "bg-red-400" },
  observed_only_gap: { title: "observed only", class: "bg-red-400" },
};

const PAGE_SIZE = 100;

async function fetchLinks(
  nodeId: string,
  direction: LinkDirection,
  depth: number,
  offset: number,
  limit: number,
): Promise<LinkExplorerResponse> {
  const p = new URLSearchParams({
    direction,
    depth: String(depth),
    offset: String(offset),
    limit: String(limit),
  });
  const r = await apiFetch(`/api/node/${encodeURIComponent(nodeId)}/links?${p}`, { silent: true });
  return (await r.json()) as LinkExplorerResponse;
}

export default function LinkExplorer(props: { nodeId: string }) {
  const [direction, setDirection] = createSignal<LinkDirection>("downstream");
  const [depth, setDepth] = createSignal(1);
  const [filterText, setFilterText] = createSignal("");
  const [rows, setRows] = createSignal<LinkRow[]>([]);
  const [truncated, setTruncated] = createSignal(false);
  const [total, setTotal] = createSignal(0);

  // Both directions' counts are loaded lazily (limit=1 — header count only,
  // no row cost) so the `[upstream N | downstream N]` toggle can show both
  // without paying for whichever direction isn't active.
  const [upstreamCount] = createResource(
    () => props.nodeId,
    (id) => fetchLinks(id, "upstream", 1, 0, 1).then((r) => r.total),
  );
  const [downstreamCount] = createResource(
    () => props.nodeId,
    (id) => fetchLinks(id, "downstream", 1, 0, 1).then((r) => r.total),
  );

  const [page] = createResource(
    () => [props.nodeId, direction(), depth()] as const,
    async ([id, dir, d]) => {
      const result = await fetchLinks(id, dir, d, 0, PAGE_SIZE);
      setRows(result.rows);
      setTotal(result.total);
      setTruncated(result.truncated);
      return result;
    },
  );

  async function loadMore() {
    const result = await fetchLinks(props.nodeId, direction(), depth(), rows().length, PAGE_SIZE);
    setRows([...rows(), ...result.rows]);
    setTruncated(result.truncated);
  }

  const filtered = createMemo(() => {
    const { chips, text } = parseQuery(filterText());
    const needle = text.toLowerCase();
    return rows().filter((r) => {
      if (chips.kind && r.type !== chips.kind) return false;
      if (chips.service && r.service !== chips.service) return false;
      if (needle && !r.label.toLowerCase().includes(needle)) return false;
      return true;
    });
  });

  // Depth>1 rows group under a "via X → Y" heading (the path back to the
  // selected node); depth-1 rows share the "" (ungrouped) key.
  const grouped = createMemo((): { via: string; rows: LinkRow[] }[] => {
    const groups = new Map<string, LinkRow[]>();
    for (const r of filtered()) {
      const key = r.via && r.via.length > 0 ? r.via.join(" → ") : "";
      const list = groups.get(key);
      if (list) list.push(r);
      else groups.set(key, [r]);
    }
    return [...groups.entries()].map(([via, rs]) => ({ via, rows: rs }));
  });

  function peek(row: LinkRow) {
    flowHighlightStore.set([props.nodeId, row.node_id]);
  }

  function clearPeek() {
    flowHighlightStore.clear();
  }

  // Budget-checked commit-expand: approximates against the canvas's current
  // rendered node-id count (canvasElementsStore doesn't separately publish
  // an edge count) rather than re-deriving the exact post-add total —
  // adding one node + one edge never moves that approximation more than 2
  // off the real figure, which is well inside BUDGET's headroom.
  function commitExpand(row: LinkRow) {
    if (canvasElementsStore.ids().size + 2 > BUDGET) return;
    const node: GraphNode = {
      id: row.node_id,
      type: row.type,
      label: row.label,
      service: row.service,
      file: row.file,
      line: row.line,
      language: "",
    };
    const edge: GraphEdge =
      direction() === "downstream"
        ? { id: row.edge_id, from: props.nodeId, to: row.node_id, type: row.edge_type, label: row.edge_label }
        : { id: row.edge_id, from: row.node_id, to: props.nodeId, type: row.edge_type, label: row.edge_label };
    expandedElementsStore.add(node, edge);
    const current = scopeStore.viewState().expanded ?? [];
    if (!current.includes(node.id)) scopeStore.setExpanded([...current, node.id]);
  }

  // Commit-navigate (UN.2): same push-file-scope + select + reveal sequence
  // as the palette's openSymbol, so `→` lands identically to picking a
  // palette symbol result.
  function commitNavigate(row: LinkRow) {
    if (row.file) {
      scopeStore.push({ kind: "file", service: row.service, path: row.file });
      selectionStore.setSelection({ kind: "node", id: row.node_id });
      treeStore.reveal(row.node_id);
    } else {
      selectionStore.setSelection({ kind: "node", id: row.node_id });
    }
  }

  return (
    <div data-testid="link-explorer" class="mt-2 border-t border-neutral-800 pt-2">
      <div class="flex items-center gap-1 mb-2">
        <button
          data-testid="link-explorer-upstream"
          class={`text-xs px-2 py-0.5 rounded ${direction() === "upstream" ? "bg-neutral-700 text-white" : "text-neutral-400 hover:text-white"}`}
          onClick={() => setDirection("upstream")}
        >
          upstream {upstreamCount() ?? "…"}
        </button>
        <button
          data-testid="link-explorer-downstream"
          class={`text-xs px-2 py-0.5 rounded ${direction() === "downstream" ? "bg-neutral-700 text-white" : "text-neutral-400 hover:text-white"}`}
          onClick={() => setDirection("downstream")}
        >
          downstream {downstreamCount() ?? "…"}
        </button>
        <select
          data-testid="link-explorer-depth"
          class="ml-auto text-xs bg-neutral-900 border border-neutral-700 rounded px-1 py-0.5"
          value={depth()}
          onChange={(e) => setDepth(Number(e.currentTarget.value))}
        >
          <option value={1}>depth 1</option>
          <option value={2}>depth 2</option>
          <option value={3}>depth 3</option>
        </select>
      </div>
      <input
        data-testid="link-explorer-filter"
        class="w-full mb-2 text-xs px-2 py-1 bg-neutral-900 border border-neutral-700 rounded text-neutral-200"
        placeholder="kind:function service:name …"
        value={filterText()}
        onInput={(e) => setFilterText(e.currentTarget.value)}
      />
      <Show when={page.loading}>
        <div class="text-xs text-neutral-400">Loading links…</div>
      </Show>
      <Show when={page.error}>
        <div class="text-xs text-neutral-400">Failed to load links.</div>
      </Show>
      <Show when={!page.loading && !page.error && filtered().length === 0}>
        <div class="text-xs text-neutral-400" data-testid="link-explorer-empty">
          No {direction()} links{filterText().trim() ? " match this filter" : ""}.
        </div>
      </Show>
      <div class="space-y-2">
        <For each={grouped()}>
          {(group) => (
            <div>
              <Show when={group.via}>
                <div class="text-[10px] text-neutral-500 mb-0.5">via {group.via}</div>
              </Show>
              <ul class="space-y-1">
                <For each={group.rows}>
                  {(row) => {
                    const dot = () => VERIFICATION_DOT[row.verification_state ?? ""] ?? null;
                    return (
                      <li
                        data-testid="link-explorer-row"
                        class="px-2 py-1.5 rounded bg-neutral-900 hover:bg-neutral-800 text-xs"
                        onMouseEnter={() => peek(row)}
                        onMouseLeave={() => clearPeek()}
                      >
                        <div class="flex items-center justify-between gap-2">
                          <span class="flex items-center gap-1.5 min-w-0">
                            <Show when={dot()}>{(d) => <span class={`w-1.5 h-1.5 rounded-full shrink-0 ${d().class}`} title={d().title} />}</Show>
                            <span class="text-neutral-200 truncate" title={row.label}>{displayLabel(row.label)}</span>
                          </span>
                          <span class="flex items-center gap-1 shrink-0">
                            <Show when={row.depth === 1}>
                              <button
                                data-testid="link-explorer-commit-expand"
                                class="text-neutral-400 hover:text-white"
                                title="Add to canvas"
                                onClick={() => commitExpand(row)}
                              >
                                ＋
                              </button>
                            </Show>
                            <button
                              data-testid="link-explorer-commit-navigate"
                              class="text-neutral-400 hover:text-white"
                              title="Go to"
                              onClick={() => commitNavigate(row)}
                            >
                              →
                            </button>
                          </span>
                        </div>
                        <div class="text-neutral-400 mt-0.5 truncate">
                          {row.edge_type}
                          {row.channel ? ` · ${row.channel}` : ""} · {row.service} ·{" "}
                          {row.file ? `${row.file}:${row.line}` : "—"}
                        </div>
                      </li>
                    );
                  }}
                </For>
              </ul>
            </div>
          )}
        </For>
      </div>
      <div class="flex items-center justify-between mt-1">
        <span class="text-[10px] text-neutral-500">
          {filtered().length} of {total()} {direction()} link{total() === 1 ? "" : "s"}
        </span>
        <Show when={truncated()}>
          <button
            data-testid="link-explorer-load-more"
            class="text-[10px] text-indigo-300 hover:text-indigo-200"
            onClick={() => void loadMore()}
          >
            load more
          </button>
        </Show>
      </div>
    </div>
  );
}
