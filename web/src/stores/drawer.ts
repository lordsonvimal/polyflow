import { createSignal } from "solid-js";

// Lifted out of BottomDrawer so other views (e.g. the tree explorer's ⚠
// unresolved-refs badge, UF.5's "Copy context" entry points, UF.6's canvas
// coverage-overlay badge) can open the drawer pre-filtered/pre-tabbed
// without owning it. Both the Context (UF.5) and Unresolved (UF.6) tabs are
// real; `unresolvedFilter` seeds the Unresolved tab's service/free-text
// query (BottomDrawer.tsx's UnresolvedTab).
export type DrawerTab = "context" | "unresolved" | "jobs";

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
  // UO.0: the Jobs tab — opened by the top-bar Index button (both its
  // click-while-running behavior and its 409 single-flight handling) and by
  // a job's auto-open on start.
  openJobs: () => {
    setActiveTab("jobs");
    setOpen(true);
  },
};
