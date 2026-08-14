import { createSignal } from "solid-js";

// UF.2: "Overlay all" renders the union of every ranked path with a
// per-path color accent — up to 5 distinct colors (index 0-4), any path
// beyond that groups into a single "more" bucket (index 5) rather than
// growing the palette unboundedly. Keyed by node id; a node shared by
// several paths keeps its *first* (best-ranked) path's color, since a
// mixed color would say nothing.
export const OVERLAY_COLOR_COUNT = 5;
export const OVERLAY_MORE_INDEX = OVERLAY_COLOR_COUNT;

const [assignment, setAssignment] = createSignal<ReadonlyMap<string, number>>(new Map());

export const pathOverlayStore = {
  assignment,
  // pathsNodeIds: ordered list (rank order) of each path's member node ids.
  set: (pathsNodeIds: string[][]) => {
    const next = new Map<string, number>();
    pathsNodeIds.forEach((ids, i) => {
      const colorIndex = Math.min(i, OVERLAY_MORE_INDEX);
      for (const id of ids) {
        if (!next.has(id)) next.set(id, colorIndex);
      }
    });
    setAssignment(next);
  },
  clear: () => setAssignment(new Map()),
};
