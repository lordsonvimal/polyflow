import { createSignal } from "solid-js";

// The set of node/edge ids currently rendered on the canvas for the active
// scope. Written by CanvasHost whenever its render set changes; read by
// anything that needs to know "is this graph element visible right now"
// without importing CanvasHost itself (e.g. the tree explorer's two-way
// sync, which must offer "open scope" instead of silently no-opping when
// a tree row's node isn't on canvas).
const [ids, setIds] = createSignal<ReadonlySet<string>>(new Set());

export const canvasElementsStore = {
  ids,
  setIds: (next: ReadonlySet<string>) => setIds(next),
  has: (id: string) => ids().has(id),
};
