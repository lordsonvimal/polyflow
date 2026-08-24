// High-level view: one node per service, cross-service edges aggregated per
// edge type with counts. Same-service edges disappear — this is the
// architecture-diagram altitude, matching the server's service-level
// Mermaid export.

import { GraphNode, GraphEdge } from "./types";

export const SERVICE_NODE_TYPE = "service";
const SERVICE_PREFIX = "service:";

export function isServiceNodeId(id: string): boolean {
  return id.startsWith(SERVICE_PREFIX);
}

// Container-scope compounds (views/canvas/scopes/container.ts) synthesize
// folder/file group ids as `folder:${service}:${path}` / `file:${service}:${path}`
// — client-side-only ids with no backing graph node, so no `/api/node/*`
// or `/api/tree`/`/api/unresolved` call can ever resolve them. Real node
// ids are always `service:file:type:name:line`, so this can only
// false-positive if a service is literally named "service", "folder", or
// "file".
export function isContainerGroupNodeId(id: string): boolean {
  return isServiceNodeId(id) || id.startsWith("folder:") || id.startsWith("file:");
}

export function serviceNodeId(service: string): string {
  return `${SERVICE_PREFIX}${service}`;
}

export function serviceFromNodeId(id: string): string {
  return id.slice(SERVICE_PREFIX.length);
}

// Tier GR: a fleet-aware idx unions a fleet's bridge.db nodes into whichever
// member's own store is currently active (buildFleetAwareIndex) — every
// bridge-copied node carries meta.owner_service (GR.2). A service with no
// node lacking that tag has no local file/folder backbone at all (the
// bridge only ever copies specific cross-service edge endpoints, never a
// full contains tree — internal/graph/tree.go's BuildTree needs a
// NodeTypeFile node to produce anything), so drilling into it always lands
// on an empty scope. Shared by aggregate.ts's overview pills and
// scopes/container.ts's foreign-service stub connectors so both know to
// decline the drill instead of silently failing.
export function isBridgeOnlyService(nodes: GraphNode[], service: string): boolean {
  let sawAny = false;
  for (const n of nodes) {
    if (n.service !== service) continue;
    sawAny = true;
    if (!n.meta?.owner_service) return false;
  }
  return sawAny;
}

export function aggregateServices(
  nodes: GraphNode[],
  edges: GraphEdge[]
): { nodes: GraphNode[]; edges: GraphEdge[] } {
  // Nodes with no service attribution can never take part in a
  // cross-service edge (the loop below drops any edge whose endpoint
  // resolves to a falsy service), so surfacing them as a floating,
  // edge-less "(unknown service)" pill is pure noise — drop them here.
  const svcByNode = new Map<string, string>();
  const counts = new Map<string, number>();
  for (const n of nodes) {
    if (!n.service) continue;
    svcByNode.set(n.id, n.service);
    counts.set(n.service, (counts.get(n.service) ?? 0) + 1);
  }

  const outNodes: GraphNode[] = [...counts.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([service, count]) => ({
      id: serviceNodeId(service),
      type: SERVICE_NODE_TYPE,
      label: service,
      service,
      file: "",
      line: 0,
      language: "",
      meta: {
        node_count: String(count),
        ...(isBridgeOnlyService(nodes, service) ? { bridge_only: "true" } : {}),
      },
    }));

  // UN.8: one edge per *unordered* service pair, not per (direction, type).
  // Two services that talk both ways (e.g. web calls polyflow's API, and
  // polyflow pushes SSE back to web) used to draw as two near-parallel
  // curves that read as duplicates rather than a single bidirectional
  // relationship — styling them apart (color/arrow/width) never survived
  // more than a glance. One pill per pair, opened via ServicePairPanel's
  // channel-list drill-in (already the click target for any agg edge), is
  // the actual fix: the "which types, which directions" detail belongs in
  // that list, not smuggled into how many near-identical lines fit between
  // two boxes.
  const agg = new Map<
    string,
    { a: string; b: string; total: number; types: Set<string>; forward: number; backward: number }
  >();
  for (const e of edges) {
    const from = svcByNode.get(e.from);
    const to = svcByNode.get(e.to);
    if (!from || !to || from === to) continue;
    // Dedupe key is order-independent (alphabetical), but a/b — what the
    // drawn edge's own from/to become — stay whichever direction was
    // observed *first*: a unidirectional pair must keep its real arrow
    // direction, not get force-flipped to alphabetical order.
    const key = from < to ? `${from}\0${to}` : `${to}\0${from}`;
    let g = agg.get(key);
    if (!g) {
      g = { a: from, b: to, total: 0, types: new Set(), forward: 0, backward: 0 };
      agg.set(key, g);
    }
    g.total++;
    g.types.add(e.type);
    if (from === g.a) g.forward++;
    else g.backward++;
  }

  const outEdges: GraphEdge[] = [...agg.values()]
    .sort((x, y) => x.a.localeCompare(y.a) || x.b.localeCompare(y.b))
    .map((g) => {
      const bidirectional = g.forward > 0 && g.backward > 0;
      const types = [...g.types].sort();
      const label = types.length > 1 ? `${g.total} edges` : g.total > 1 ? `${types[0]} ×${g.total}` : types[0];
      return {
        id: `agg:${g.a}-${g.b}`,
        from: serviceNodeId(g.a),
        to: serviceNodeId(g.b),
        type: types.length === 1 ? types[0] : "cross_service",
        label,
        meta: { bidirectional: bidirectional ? "true" : "false" },
      };
    });

  return { nodes: outNodes, edges: outEdges };
}
