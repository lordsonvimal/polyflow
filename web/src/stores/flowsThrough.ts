import { createSignal } from "solid-js";

// UF.1: bridges the canvas context-menu action ("Isolate flows through
// here") to DetailHost's node section, which owns the actual panel. The
// context menu only knows a node id and a click point — it can't render a
// panel itself, so it selects the node and leaves a one-shot request here;
// DetailHost auto-expands its ThroughPanel when the newly-selected node
// matches, then consumes the request so it never re-fires on later selects.
const [requestedNodeId, setRequestedNodeId] = createSignal<string | null>(null);

export const flowsThroughStore = {
  requestedNodeId,
  request: (nodeId: string) => setRequestedNodeId(nodeId),
  consume: () => setRequestedNodeId(null),
};
