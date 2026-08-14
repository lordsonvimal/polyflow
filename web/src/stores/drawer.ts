import { createSignal } from "solid-js";

// Lifted out of BottomDrawer so other views (e.g. the tree explorer's ⚠
// unresolved-refs badge, UF.5's "Copy context" entry points) can open the
// drawer pre-filtered/pre-tabbed without owning it. The Unresolved tab's
// content itself lands in plan 13; today the drawer only surfaces the
// pending filter honestly rather than pretending to filter a tab that
// doesn't exist yet. The Context tab (UF.5) is real.
export type DrawerTab = "context" | "unresolved";

const [open, setOpen] = createSignal(false);
const [activeTab, setActiveTab] = createSignal<DrawerTab>("context");
const [unresolvedFilter, setUnresolvedFilter] = createSignal<{ service: string; path: string } | undefined>(undefined);

export const drawerStore = {
  open,
  setOpen,
  activeTab,
  setActiveTab,
  unresolvedFilter,
  openUnresolvedFor: (service: string, path: string) => {
    setUnresolvedFilter({ service, path });
    setActiveTab("unresolved");
    setOpen(true);
  },
  openContext: () => {
    setActiveTab("context");
    setOpen(true);
  },
};
