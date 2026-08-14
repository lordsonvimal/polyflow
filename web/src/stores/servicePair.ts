import { createSignal } from "solid-js";

// UF.3: single-click drill-in on an aggregated overview service-pair edge.
// The clicked cytoscape element is a synthetic `agg:` id (lib/aggregate.ts)
// with no backend counterpart, so — unlike flowsThroughStore/pathFinderStore
// — this store carries the resolved (from, to) service pair directly rather
// than a node id DetailHost would need to re-derive.
export interface ServicePair {
  from: string;
  to: string;
  edgeId: string;
}

const [pair, setPair] = createSignal<ServicePair | null>(null);

export const servicePairStore = {
  pair,
  open: (from: string, to: string, edgeId: string) => setPair({ from, to, edgeId }),
  close: () => setPair(null),
};
