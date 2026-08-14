import { createSignal } from "solid-js";

// UF.8: bridges the palette's "Explore links" result action to DetailHost's
// node section, which owns the actual LinkExplorer panel — same one-shot
// request/consume bridge shape as flowsThroughStore (UF.1) and pathFinderStore
// (UF.2). The palette only knows the target node id; DetailHost auto-expands
// its LinkExplorer toggle for it, then consumes the request so it never
// re-fires on a later, unrelated selection.
const [requestedNodeId, setRequestedNodeId] = createSignal<string | null>(null);

export const linkExplorerStore = {
  requestedNodeId,
  request: (nodeId: string) => setRequestedNodeId(nodeId),
  consume: () => setRequestedNodeId(null),
};
