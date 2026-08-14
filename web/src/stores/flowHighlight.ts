import { createSignal } from "solid-js";

// UF.1: cheap hover pre-highlight for "flows through here" rows — the
// canvas dims everything except the hovered flow's member ids, without any
// layout call (CanvasHost adds/removes two classes off this set). Cleared
// on mouse-leave, so it never survives a scope change by accident.
const [ids, setIds] = createSignal<ReadonlySet<string>>(new Set());

export const flowHighlightStore = {
  ids,
  set: (next: Iterable<string>) => setIds(new Set(next)),
  clear: () => setIds(new Set()),
};
