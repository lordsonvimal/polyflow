// The landing graph scope: one node per service, cross-service edges
// aggregated per edge type with counts ("http ×12 · rabbitmq ×2").
import { aggregateServices } from "../../../lib/aggregate";
import { GraphData, fetchAllGraph, sortGraphData } from "./common";

export async function resolveOverview(signal?: AbortSignal): Promise<GraphData> {
  const all = await fetchAllGraph(signal);
  return sortGraphData(aggregateServices(all.nodes, all.edges));
}
