import { createSignal } from "solid-js";

// Lifted out of BottomDrawer so other views (e.g. the tree explorer's ⚠
// unresolved-refs badge, UF.5's "Copy context" entry points, UF.6's canvas
// coverage-overlay badge) can open the drawer pre-filtered/pre-tabbed
// without owning it. Both the Context (UF.5) and Unresolved (UF.6) tabs are
// real; `unresolvedFilter` seeds the Unresolved tab's service/free-text
// query (BottomDrawer.tsx's UnresolvedTab).
export type DrawerTab = "context" | "unresolved" | "jobs" | "toolcalls";

export const DRAWER_DEFAULT_HEIGHT = 260;
export const DRAWER_MIN_HEIGHT = 140;

const [open, setOpen] = createSignal(false);
const [activeTab, setActiveTab] = createSignal<DrawerTab>("context");
// Resizable via BottomDrawer's drag handle; kept here (not local component
// state) so the height survives the drawer being toggled closed/open again.
const [height, setHeight] = createSignal(DRAWER_DEFAULT_HEIGHT);
const [unresolvedFilter, setUnresolvedFilter] = createSignal<{ service: string; path: string } | undefined>(undefined);
// UO.3: the Health dashboard's Unresolved card links through by kind only
// (no service/path context there), so it gets its own seed signal rather
// than overloading unresolvedFilter's service/path shape.
const [unresolvedKindFilter, setUnresolvedKindFilter] = createSignal<string | undefined>(undefined);

export const drawerStore = {
  open,
  setOpen,
  activeTab,
  setActiveTab,
  height,
  setHeight: (h: number) => setHeight(Math.max(DRAWER_MIN_HEIGHT, h)),
  unresolvedFilter,
  unresolvedKindFilter,
  openUnresolvedFor: (service: string, path: string) => {
    setUnresolvedFilter({ service, path });
    setActiveTab("unresolved");
    setOpen(true);
  },
  openUnresolvedByKind: (kind: string) => {
    setUnresolvedKindFilter(kind);
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
  // UO.1: the tool-call log tab (top-bar/menu entry points and the debug
  // question "what did my agent just ask?" land here).
  openToolCalls: () => {
    setActiveTab("toolcalls");
    setOpen(true);
  },
};
