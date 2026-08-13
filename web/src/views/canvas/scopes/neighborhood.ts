// A depth-bounded neighborhood around one node, via /api/graph/trace
// (context.Build-backed). Depth comes from Scope.depth — the detail-panel
// stepper (1-5, default 2) that sets it is plan-10's DetailHost, out of
// scope here.
import { Scope } from "../../../stores/scope";
import { apiFetch } from "../../../lib/apiFetch";
import { GraphData, parseCytoGraph, sortGraphData } from "./common";

export async function resolveNeighborhood(
  scope: Extract<Scope, { kind: "neighborhood" }>,
  signal?: AbortSignal,
): Promise<GraphData> {
  const p = new URLSearchParams({ root: scope.nodeId, direction: "both", depth: String(scope.depth) });
  const r = await apiFetch(`/api/graph/trace?${p}`, { signal, silent: true });
  return sortGraphData(parseCytoGraph(await r.json()));
}
