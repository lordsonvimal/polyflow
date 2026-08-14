// UF.4: the induced subgraph over a marquee/shift-click multi-selection —
// exactly the selected nodes plus every edge with both endpoints inside the
// selection (never a boundary/stub edge; a group is a closed set by
// definition). Same fetchAllGraph + client-side filter shape as
// container.ts, since there's no dedicated /api/scope?kind=group endpoint.
import { Scope } from "../../../stores/scope";
import { GraphData, fetchAllGraph, sortGraphData } from "./common";

export async function resolveGroup(
  scope: Extract<Scope, { kind: "group" }>,
  signal?: AbortSignal,
): Promise<GraphData> {
  const ids = new Set(scope.nodeIds);
  const all = await fetchAllGraph(signal);
  const nodes = all.nodes.filter((n) => ids.has(n.id));
  const edges = all.edges.filter((e) => ids.has(e.from) && ids.has(e.to));
  return sortGraphData({ nodes, edges });
}
