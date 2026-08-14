import { createSignal } from "solid-js";

// The set of node ids currently rendered on the canvas for the active scope
// (despite the generic name, CanvasHost only ever publishes node ids here —
// see its "Publish the active scope's rendered node ids" effect). Read by
// anything that needs to know "is this graph element visible right now"
// without importing CanvasHost itself: the tree explorer's two-way sync
// (offers "open scope" instead of silently no-opping when a tree row's node
// isn't on canvas) and UF.4's "Add all matches" (unions these into the
// multi-selection).
const [ids, setIds] = createSignal<ReadonlySet<string>>(new Set());

export const canvasElementsStore = {
  ids,
  setIds: (next: ReadonlySet<string>) => setIds(next),
  has: (id: string) => ids().has(id),
};
