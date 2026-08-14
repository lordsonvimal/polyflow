// UF.4: the detail-panel section for a group scope — relationship summary
// (edge-type counts, services touched, shared channels, contained files)
// plus a per-pair interconnection matrix for small groups. Independently
// resolves the same induced subgraph CanvasHost renders (SeamSummary/
// PathFinderPanel do the same — the detail panel never reaches into
// CanvasHost's local resource).
import { createMemo, createResource, For, Show } from "solid-js";
import { resolveGroup } from "../canvas/scopes/group";
import type { GraphNode, GraphEdge } from "../../lib/types";
import { displayLabel } from "../../lib/location";

// Per-pair matrix only renders below this size — an N×N grid stops being
// "compact" well before real budget limits kick in.
const MATRIX_MAX_NODES = 8;

function typeGlyph(type: string): string {
  return type
    .split("_")
    .map((w) => w[0]?.toUpperCase() ?? "")
    .join("");
}

export function edgeTypeCounts(edges: GraphEdge[]): Map<string, number> {
  const counts = new Map<string, number>();
  for (const e of edges) counts.set(e.type, (counts.get(e.type) ?? 0) + 1);
  return counts;
}

export function servicesTouched(nodes: GraphNode[]): string[] {
  return [...new Set(nodes.map((n) => n.service).filter(Boolean))].sort();
}

export function containedFiles(nodes: GraphNode[]): string[] {
  return [...new Set(nodes.map((n) => n.file).filter(Boolean))].sort();
}

export function sharedChannels(nodes: GraphNode[]): string[] {
  return nodes.filter((n) => n.type === "channel").map((n) => n.label).sort();
}

// Cell = distinct edge-type glyphs between the two nodes, either direction,
// joined "·" — direction isn't lost (the raw edges are still on canvas),
// this cell is purely "are they connected, and how".
export function matrixCell(edges: GraphEdge[], a: string, b: string): string {
  const types = new Set(
    edges.filter((e) => (e.from === a && e.to === b) || (e.from === b && e.to === a)).map((e) => e.type),
  );
  return [...types].sort().map(typeGlyph).join("·");
}

export default function GroupSummary(props: { nodeIds: string[] }) {
  const [data] = createResource(
    () => props.nodeIds,
    (ids) => resolveGroup({ kind: "group", nodeIds: ids }),
  );

  const nodes = createMemo(() => data()?.nodes ?? []);
  const edges = createMemo(() => data()?.edges ?? []);
  const showMatrix = createMemo(() => nodes().length > 0 && nodes().length <= MATRIX_MAX_NODES);

  return (
    <div data-testid="group-summary" class="mt-2 border-t border-neutral-800 pt-2">
      <Show when={data.loading}>
        <div class="text-xs text-neutral-400">Loading group…</div>
      </Show>
      <Show when={data.error}>
        <div class="text-xs text-neutral-400">Failed to load group.</div>
      </Show>
      <Show when={data()}>
        <div class="text-xs text-neutral-300 space-y-2">
          <div class="text-neutral-200">
            {nodes().length} node{nodes().length === 1 ? "" : "s"} · {edges().length} edge{edges().length === 1 ? "" : "s"}
          </div>

          <Show when={edgeTypeCounts(edges()).size > 0}>
            <div>
              <div class="text-neutral-400 mb-1">Edge types</div>
              <ul class="space-y-0.5">
                <For each={[...edgeTypeCounts(edges())].sort((a, b) => a[0].localeCompare(b[0]))}>
                  {([type, count]) => (
                    <li>
                      {type} × {count}
                    </li>
                  )}
                </For>
              </ul>
            </div>
          </Show>

          <div>
            <div class="text-neutral-400 mb-1">Services touched</div>
            <div>{servicesTouched(nodes()).join(", ") || "—"}</div>
          </div>

          <Show when={sharedChannels(nodes()).length > 0}>
            <div>
              <div class="text-neutral-400 mb-1">Shared channels</div>
              <div>{sharedChannels(nodes()).join(", ")}</div>
            </div>
          </Show>

          <div>
            <div class="text-neutral-400 mb-1">Contained files</div>
            <ul class="space-y-0.5">
              <For each={containedFiles(nodes())}>
                {(f) => <li class="break-all">{f}</li>}
              </For>
            </ul>
          </div>

          <Show when={showMatrix()}>
            <div>
              <div class="text-neutral-400 mb-1">Interconnections</div>
              <table data-testid="group-matrix" class="text-[10px] border-collapse">
                <thead>
                  <tr>
                    <th class="p-0.5" />
                    <For each={nodes()}>
                      {(n) => (
                        <th class="p-0.5 text-neutral-400 font-normal truncate max-w-[40px]" title={n.label}>
                          {displayLabel(n.label)}
                        </th>
                      )}
                    </For>
                  </tr>
                </thead>
                <tbody>
                  <For each={nodes()}>
                    {(row) => (
                      <tr>
                        <th class="p-0.5 text-neutral-400 font-normal text-right truncate max-w-[60px]" title={row.label}>
                          {displayLabel(row.label)}
                        </th>
                        <For each={nodes()}>
                          {(col) => (
                            <td class="p-0.5 text-center border border-neutral-800">
                              {row.id === col.id ? "" : matrixCell(edges(), row.id, col.id)}
                            </td>
                          )}
                        </For>
                      </tr>
                    )}
                  </For>
                </tbody>
              </table>
            </div>
          </Show>
        </div>
      </Show>
    </div>
  );
}
