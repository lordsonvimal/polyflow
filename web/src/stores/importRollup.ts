import { createSignal } from "solid-js";
import { GraphEdge } from "../lib/types";

// UN.5: the Imports lens's module rollup collapses concrete import edges
// into one file→file edge per pair (lenses.ts's aggregateImportsRollup).
// CanvasHost publishes the rollup id -> concrete-edges map here whenever the
// rollup is active, so DetailHost can drill an aggregated edge selection
// back into its constituent import list without CanvasHost and DetailHost
// needing to share Cytoscape state directly.
const [detail, setDetail] = createSignal<Map<string, GraphEdge[]>>(new Map());

export const importRollupStore = {
  detail,
  setDetail,
  get: (rollupEdgeId: string): GraphEdge[] | undefined => detail().get(rollupEdgeId),
};
