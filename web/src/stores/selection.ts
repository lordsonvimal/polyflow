import { createSignal } from "solid-js";

// label is optional display metadata for an edge selection — populated by
// CanvasHost from the clicked cytoscape element's own `label` data so a
// generic detail-panel fallback (an `agg:` edge with no dedicated drill-in,
// e.g. a container-scope stub connector) can show *something* beyond a bare
// id, rather than silently rendering an empty body.
export type Selection = { kind: "node" | "edge"; id: string; label?: string } | null;

// Extends the base target with optional display metadata (populated by canvas in US.3)
export type HoverTarget = {
  kind: "node" | "edge";
  id: string;
  label?: string;
  file?: string;
  line?: number;
  end_line?: number;
} | null;

const [selection, setSelection] = createSignal<Selection>(null);
const [hoverTarget, setHoverTarget] = createSignal<HoverTarget>(null);
const [pinned, setPinned] = createSignal<NonNullable<Selection>[]>([]);

function pin(sel: NonNullable<Selection>) {
  setPinned(prev =>
    prev.length < 2 && !prev.find(p => p.id === sel.id) ? [...prev, sel] : prev
  );
}

function unpin(id: string) {
  setPinned(prev => prev.filter(p => p.id !== id));
}

export const selectionStore = {
  selection, setSelection,
  hoverTarget, setHoverTarget,
  pinned, pin, unpin,
};
