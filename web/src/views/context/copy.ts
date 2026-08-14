// UF.5: the single module every "Copy context" entry point routes through
// (detail panel, flow chip menu, group HUD, scope breadcrumb menu, ⌘⇧C) —
// builds the UB.6 POST /api/context/bundle request and fetches it. Never
// reimplements traversal: a source is just "which UB.6 elements" plus the
// mode/depth/snippets/budget knobs the caller already holds.
import { apiFetchJSON } from "../../lib/apiFetch";
import { isServiceNodeId } from "../../lib/aggregate";
import { canvasElementsStore } from "../../stores/canvasElements";
import type { Selection } from "../../stores/selection";
import type { FlowRef } from "../../stores/scope";
import { SEAM_CHANNEL_PREFIX, type FlowChain } from "../canvas/scopes/flow";

export type CopyMode = "viewed" | "expanded";

export interface BundleElement {
  kind: "node" | "edge" | "flow" | "group";
  ids: string[];
}

export interface BundleRequest {
  elements: BundleElement[];
  mode: CopyMode;
  depth: number;
  snippets: boolean;
  max_tokens: number;
}

export interface BundleResponse {
  markdown: string;
  tokens_estimate: number;
  truncated: boolean;
  omitted: string[];
}

export type CopySource =
  | { kind: "node"; id: string }
  | { kind: "edge"; id: string }
  | { kind: "flow"; id: string }
  | { kind: "group"; ids: string[] }
  | { kind: "scope" };

export interface CopyOptions {
  mode: CopyMode;
  depth: number;
  snippets: boolean;
  maxTokens: number;
}

export interface RequestPreview {
  request: BundleRequest;
  // Set only for a "scope" source with clustered (collapsed-file) members
  // expanded back to their real ids — the plan's pinned preview line, e.g.
  // "142 nodes (3 clusters expanded)".
  note?: string;
}

export function buildRequest(source: CopySource, opts: CopyOptions): RequestPreview {
  const base = { mode: opts.mode, depth: opts.depth, snippets: opts.snippets, max_tokens: opts.maxTokens };
  switch (source.kind) {
    case "node":
      return { request: { elements: [{ kind: "node", ids: [source.id] }], ...base } };
    case "edge":
      return { request: { elements: [{ kind: "edge", ids: [source.id] }], ...base } };
    case "flow":
      return { request: { elements: [{ kind: "flow", ids: [source.id] }], ...base } };
    case "group":
      return { request: { elements: [{ kind: "group", ids: source.ids }], ...base } };
    case "scope": {
      const raw = [...canvasElementsStore.ids()];
      const { ids, clusterCount } = canvasElementsStore.expand(raw);
      const note = `${ids.length} node${ids.length === 1 ? "" : "s"}` + (clusterCount > 0
        ? ` (${clusterCount} cluster${clusterCount === 1 ? "" : "s"} expanded)`
        : "");
      return { request: { elements: [{ kind: "node", ids }], ...base }, note };
    }
  }
}

// Maps a flow chip's FlowRef to a CopySource. Only "seam" and "through" have
// a direct UB.6 reading (an edge's seam closure, and a flow-through node
// respectively — see contextbundle.go's buildEdgeBlock/buildFlowBlock); the
// remaining kinds (path/waypoints/varflow/edgeset/pins) have no single-id
// UB.6 shape, so they fall back to "group" over the resolved chain's real
// hop node ids (the synthetic seam-channel pill has no backing node and is
// excluded).
export function flowRefToSource(flow: FlowRef, chains: FlowChain[]): CopySource {
  switch (flow.kind) {
    case "seam":
      return { kind: "edge", id: flow.edgeId };
    case "through":
    case "varflow":
      return { kind: "flow", id: flow.nodeId };
    case "path":
    case "waypoints":
    case "edgeset":
    case "pins": {
      const seen = new Set<string>();
      for (const chain of chains) {
        for (const hop of chain.hops) {
          if (!hop.nodeId.startsWith(SEAM_CHANNEL_PREFIX)) seen.add(hop.nodeId);
        }
      }
      return { kind: "group", ids: [...seen] };
    }
  }
}

// ⌘⇧C's "current selection" priority: a real single selection first, else
// a live multi-select (2+ marquee/shift-click nodes), else the committed
// group scope, else the committed flow scope — the same fallback order a
// user reading the canvas would expect "what's selected" to mean. Returns
// null when there's nothing copyable (never guesses).
export function selectionCopySource(
  selection: Selection,
  multiSelectIds: ReadonlySet<string>,
  topScope: { kind: string; nodeIds?: string[]; flow?: FlowRef } | undefined,
): CopySource | null {
  if (selection && !selection.id.startsWith("agg:") && !selection.id.startsWith("rollup:")) {
    if (!(selection.kind === "node" && isServiceNodeId(selection.id))) {
      return { kind: selection.kind, id: selection.id };
    }
  }
  if (multiSelectIds.size >= 2) {
    return { kind: "group", ids: [...multiSelectIds].sort() };
  }
  if (topScope?.kind === "group" && topScope.nodeIds) {
    return { kind: "group", ids: topScope.nodeIds };
  }
  if (topScope?.kind === "flow" && topScope.flow) {
    return flowRefToSource(topScope.flow, []);
  }
  return null;
}

export async function fetchBundle(req: BundleRequest): Promise<BundleResponse> {
  return apiFetchJSON<BundleResponse>("/api/context/bundle", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
}
