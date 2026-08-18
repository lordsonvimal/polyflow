import { createSignal } from "solid-js";
import type { Core } from "cytoscape";

// UO.5: the live Cytoscape instance, published by CanvasHost so the Share
// menu (TopBar, outside the canvas subtree) can render PNG/SVG/JSON exports
// of exactly what's on screen (filters, collapse state, layout) without
// CanvasHost importing export UI, or the export code re-deriving canvas
// state from scratch. A plain object signal — the instance itself isn't
// reactive data, just an imperative handle.
const [cy, setCy] = createSignal<Core | null>(null);

export const canvasRefStore = {
  cy,
  set: (instance: Core | null) => setCy(instance),
};
