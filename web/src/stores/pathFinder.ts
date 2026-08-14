import { createSignal } from "solid-js";

export interface PathNodeRef {
  id: string;
  label: string;
}

// UF.2: "Set as path start" pins node A (persistent, shown as a top-bar
// chip); "Find paths from A" on a second node bridges to PathFinderPanel
// the same one-shot way UF.1's flowsThroughStore bridges the context menu
// to DetailHost — the menu only knows a click point, not a panel.
const [startNode, setStartNode] = createSignal<PathNodeRef | null>(null);
const [requestedTo, setRequestedTo] = createSignal<PathNodeRef | null>(null);

export const pathFinderStore = {
  startNode,
  requestedTo,
  setStart: (ref: PathNodeRef) => setStartNode(ref),
  clearStart: () => setStartNode(null),
  requestPaths: (ref: PathNodeRef) => setRequestedTo(ref),
  consume: () => setRequestedTo(null),
};
