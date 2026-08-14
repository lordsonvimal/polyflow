// UF.6: blast-radius canvas — /api/graph/trace gives the raw subgraph, this
// module layers depth rings on top of it (target/direct/transitive) so
// CanvasHost's stylesheet can style them without a server change. Direction
// is UI-facing "up" (dependents/callers) / "down" (dependencies/reaches) /
// "both"; the server's /api/graph/trace speaks forward/backward.
import { Scope } from "../../../stores/scope";
import { apiFetch } from "../../../lib/apiFetch";
import { GraphData, parseCytoGraph, sortGraphData } from "./common";

const DIRECTION_PARAM: Record<"up" | "down" | "both", string> = {
  up: "backward",
  down: "forward",
  both: "both",
};

export type ImpactRole = "target" | "direct" | "transitive";

// Pure — BFS distance from root over the induced edge set, oriented per
// `direction` ("both" walks edges as undirected, matching the server's own
// forward+backward union for that case).
export function computeImpactDepths(
  edges: { from: string; to: string }[],
  root: string,
  direction: "up" | "down" | "both",
): Map<string, number> {
  const adj = new Map<string, string[]>();
  const link = (a: string, b: string) => {
    const list = adj.get(a);
    if (list) list.push(b);
    else adj.set(a, [b]);
  };
  for (const e of edges) {
    if (direction === "down") link(e.from, e.to);
    else if (direction === "up") link(e.to, e.from);
    else {
      link(e.from, e.to);
      link(e.to, e.from);
    }
  }

  const dist = new Map<string, number>([[root, 0]]);
  const queue = [root];
  while (queue.length > 0) {
    const cur = queue.shift()!;
    const d = dist.get(cur)!;
    for (const next of adj.get(cur) ?? []) {
      if (!dist.has(next)) {
        dist.set(next, d + 1);
        queue.push(next);
      }
    }
  }
  return dist;
}

export function roleForDepth(depth: number): ImpactRole {
  if (depth === 0) return "target";
  if (depth === 1) return "direct";
  return "transitive";
}

export async function resolveImpact(
  scope: Extract<Scope, { kind: "impact" }>,
  signal?: AbortSignal,
): Promise<GraphData> {
  const p = new URLSearchParams({
    root: scope.target,
    direction: DIRECTION_PARAM[scope.direction],
    depth: String(scope.depth),
  });
  const r = await apiFetch(`/api/graph/trace?${p}`, { signal, silent: true });
  const data = parseCytoGraph(await r.json());
  const depths = computeImpactDepths(data.edges, scope.target, scope.direction);

  const nodes = data.nodes.map((n) => {
    const d = depths.get(n.id);
    if (d === undefined) return n;
    return {
      ...n,
      meta: { ...(n.meta ?? {}), impact_role: roleForDepth(d), impact_depth: String(d) },
    };
  });

  return sortGraphData({ nodes, edges: data.edges });
}
