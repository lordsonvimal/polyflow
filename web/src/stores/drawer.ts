import { createSignal } from "solid-js";

// Lifted out of BottomDrawer so other views (e.g. the tree explorer's ⚠
// unresolved-refs badge) can open the drawer pre-filtered without owning
// it. The Unresolved/Jobs/Tool-calls tab content itself lands in plan 13;
// today the drawer only surfaces the pending filter honestly rather than
// pretending to filter a tab that doesn't exist yet.
const [open, setOpen] = createSignal(false);
const [unresolvedFilter, setUnresolvedFilter] = createSignal<{ service: string; path: string } | undefined>(undefined);

export const drawerStore = {
  open,
  setOpen,
  unresolvedFilter,
  openUnresolvedFor: (service: string, path: string) => {
    setUnresolvedFilter({ service, path });
    setOpen(true);
  },
};
