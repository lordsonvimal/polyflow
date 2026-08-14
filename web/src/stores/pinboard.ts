// UF.7: pinboard chip state. Wraps scopeStore's URL-persisted `pins` field
// (rather than a plain module signal like waypointBuilderStore) — the plan
// requires pins to be shareable and to survive scope changes, which only
// scopeStore's ViewState gives for free.
//
// NOT the same feature as selectionStore's `pin`/`pinned` ("pin to compare",
// capped at 2, ephemeral, DetailHost's existing "📌 pin" button) — that
// shipped earlier and this deliberately doesn't touch it. Distinct store,
// distinct label ("pin to pinboard") wherever both appear in the UI.
import { scopeStore, PinRef } from "./scope";

function pins(): PinRef[] {
  return scopeStore.viewState().pins ?? [];
}

function isPinned(id: string): boolean {
  return pins().some((p) => p.id === id);
}

function pin(ref: PinRef): void {
  if (isPinned(ref.id)) return;
  scopeStore.setPins([...pins(), ref]);
}

function unpin(id: string): void {
  scopeStore.setPins(pins().filter((p) => p.id !== id));
}

function toggle(ref: PinRef): void {
  if (isPinned(ref.id)) unpin(ref.id);
  else pin(ref);
}

function clear(): void {
  scopeStore.setPins([]);
}

export const pinboardStore = {
  pins,
  isPinned,
  pin,
  unpin,
  toggle,
  clear,
  // "1 pin only badges" — pinboard mode (canvas fade + intersection) only
  // engages at 2+ pins.
  active: () => pins().length >= 2,
};
