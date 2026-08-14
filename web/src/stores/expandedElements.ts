import { createSignal } from "solid-js";
import type { GraphNode, GraphEdge } from "../lib/types";

// UF.8: commit-expand (`＋`) payload cache. scopeStore.viewState().expanded
// is the canonical, URL-persisted id list (what's expanded); this cache
// holds the actual node+edge objects LinkExplorer already has in hand at
// the moment of the click, so CanvasHost can splice them into the rendered
// graph without a second round-trip. Not URL-persisted itself (like
// canvasElementsStore's clusters map) — a reload without this cache simply
// drops the expansion, same honest degrade as clusters.
const [entries, setEntries] = createSignal<ReadonlyMap<string, { node: GraphNode; edge: GraphEdge }>>(new Map());

export const expandedElementsStore = {
  entries,
  add: (node: GraphNode, edge: GraphEdge) => {
    const next = new Map(entries());
    next.set(node.id, { node, edge });
    setEntries(next);
  },
  remove: (nodeId: string) => {
    const next = new Map(entries());
    next.delete(nodeId);
    setEntries(next);
  },
  clear: () => setEntries(new Map()),
};
