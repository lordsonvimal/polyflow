import { createSignal } from "solid-js";

// UF.4: the marquee-drag / shift-click multi-selection that feeds the
// "N selected — View as group" HUD chip. On canvas this mirrors Cytoscape's
// own native additive-selection state (CanvasHost's select/unselect
// listener calls setIds with the live `:selected` node set — box-drag never
// goes through gestures.ts's intent pipeline, so that listener is the only
// path for marquee). Off canvas (e.g. a tree-row shift-click) there's no
// Cytoscape selection to mirror, so gestures.ts's multiAdd case calls
// toggle() directly instead.
const [ids, setIds] = createSignal<ReadonlySet<string>>(new Set());

function toggle(id: string): void {
  setIds((prev) => {
    const next = new Set(prev);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    return next;
  });
}

function addAll(newIds: Iterable<string>, cap: number): { added: number; capped: boolean } {
  const next = new Set(ids());
  let capped = false;
  for (const id of newIds) {
    if (next.size >= cap && !next.has(id)) { capped = true; break; }
    next.add(id);
  }
  const added = next.size - ids().size;
  setIds(next);
  return { added, capped };
}

function clear(): void {
  setIds(new Set());
}

export const multiSelectStore = {
  ids,
  setIds: (next: ReadonlySet<string>) => setIds(next),
  toggle,
  addAll,
  clear,
};
