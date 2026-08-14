import { createSignal } from "solid-js";
import { selectionStore } from "./selection";

// UF.0: every way a flow can be pinned down. `label` is never stored here —
// it's derived from the resolved chain (entrypoint/terminus labels), so the
// ref itself stays a pure, URL-encodable identifier.
export type FlowRef =
  | { kind: "through"; nodeId: string; entrypointId: string }
  | { kind: "path"; from: string; to: string; index: number }
  | { kind: "waypoints"; ids: string[]; direction: "forward" | "backward" }
  | { kind: "seam"; edgeId: string }
  | { kind: "varflow"; nodeId: string }
  | { kind: "edgeset"; nodeId: string; edgeTypes: string[] } // lens-scoped flows from a node
  | { kind: "pins"; ids: string[] }; // pinboard (UF.7)

// UF.7: a pinboard chip. Unlike FlowRef's bare ids (label derived from the
// resolved chain), the tray needs a label *before* any chain resolves —
// there's no flow yet with only one pin, and the label has to survive an
// unreachable/broken-pair result too — so it's carried alongside the id.
export interface PinRef {
  id: string;
  label: string;
}

export type NodeKind =
  | "file" | "function" | "class" | "variable"
  | "http" | "amqp" | "dom" | "interface" | "method"
  | "type" | "constant" | "module";

export type Scope =
  | { kind: "search"; nodeType?: NodeKind; q?: string }
  | { kind: "overview" }
  | { kind: "service"; service: string }
  | { kind: "folder"; service: string; path: string }
  | { kind: "file"; service: string; path: string }
  | { kind: "neighborhood"; nodeId: string; depth: number }
  | { kind: "impact"; target: string; direction: "up" | "down" | "both"; depth: number }
  | { kind: "flow"; flow: FlowRef }
  | { kind: "group"; nodeIds: string[] };

export type ViewState = {
  stack: Scope[];
  isolation?: FlowRef;
  filters: { confidence: string[]; edgeTypes: string[]; services: string[] };
  selection?: { kind: "node" | "edge"; id: string };
  layout?: string;
  // UN.5 flow lens — independent of filters.edgeTypes (FilterBar's coarse
  // six-group chips, lib/edgeGroups.ts): the lens table is finer-grained and
  // the two axes compose (lens narrows first, chips further narrow), so they
  // can't share one array without collapsing two different vocabularies into
  // one. Undefined decodes as the "All" lens (views/canvas/lenses.ts).
  lens?: string;
  // "hide unlinked" toggle: when true, nodes with zero visible edges under
  // the active lens are removed instead of dimmed to 30%.
  lensHideUnlinked?: boolean;
  // Imports lens only: aggregate to file→file import edges with counts.
  lensRollup?: boolean;
  // UF.6: coverage overlay (verification-state edge styling + ⚠ unresolved
  // badges) toggle in FilterBar. Undefined decodes as "on" (default-on,
  // same convention as lensHideUnlinked/lensRollup's absent-means-off, but
  // inverted here since the overlay ships default-on).
  coverageOverlay?: boolean;
  // UF.7: pinboard chips. Deliberately a top-level ViewState field, not
  // scope-stack state — pins "survive scope changes" (plan text) and are
  // never pushed/popped, so they live beside `isolation`, not inside `stack`.
  pins?: PinRef[];
  // UF.8: link explorer commit-expand (`＋`) target ids, added to whatever
  // scope is on top of the stack. Like `pins`, a top-level field rather than
  // scope-stack state — expansion accretes across scope-local browsing and
  // is budget-checked by the caller before being committed here, not undone
  // by a push/pop.
  expanded?: string[];
};

export const DEFAULT_STATE: ViewState = {
  stack: [{ kind: "overview" }],
  filters: { confidence: [], edgeTypes: [], services: [] },
};

// Versioned URL codec
const VERSION = 1;

export function encodeViewState(state: ViewState): string {
  const json = JSON.stringify({ v: VERSION, ...state });
  return btoa(json).replace(/\+/g, "-").replace(/\//g, "_").replace(/=/g, "");
}

export function decodeViewState(s: string): { state: ViewState; unknownVersion: boolean } | null {
  try {
    const json = atob(s.replace(/-/g, "+").replace(/_/g, "/"));
    const obj = JSON.parse(json) as Record<string, unknown>;
    if (typeof obj.v !== "number") return null;
    if (obj.v !== VERSION) return { state: DEFAULT_STATE, unknownVersion: true };
    const { v: _, ...rest } = obj;
    return { state: rest as ViewState, unknownVersion: false };
  } catch {
    return null;
  }
}

function readHash(): { state: ViewState; unknownVersion: boolean } {
  if (typeof location === "undefined") return { state: DEFAULT_STATE, unknownVersion: false };
  const hash = location.hash;
  if (!hash.startsWith("#v=")) return { state: DEFAULT_STATE, unknownVersion: false };
  const result = decodeViewState(hash.slice(3));
  return result ?? { state: DEFAULT_STATE, unknownVersion: false };
}

const initial = readHash();

const [viewState, setViewState] = createSignal<ViewState>(initial.state);
const [unknownVersionNotice, setUnknownVersionNotice] = createSignal(initial.unknownVersion);
const [staleIdNotice, setStaleIdNotice] = createSignal(false);

let hashTimer: ReturnType<typeof setTimeout> | undefined;

function syncHash(state: ViewState) {
  clearTimeout(hashTimer);
  hashTimer = setTimeout(() => {
    if (typeof history !== "undefined") {
      history.replaceState(null, "", "#v=" + encodeViewState(state));
    }
  }, 250);
}

function commit(next: ViewState) {
  setViewState(next);
  syncHash(next);
}

// US.5: every scope-stack change cancels in-flight fetches from the scope
// being left, so a slow response can never paint over the new scope.
let scopeController = new AbortController();

function abortInFlight(): void {
  scopeController.abort();
  scopeController = new AbortController();
}

function commitStackChange(next: ViewState): void {
  abortInFlight();
  commit(next);
}

export const scopeStore = {
  stack: () => viewState().stack,
  viewState,
  unknownVersionNotice,
  staleIdNotice,
  dismissVersionNotice: () => setUnknownVersionNotice(false),
  dismissStaleIdNotice: () => setStaleIdNotice(false),

  // AbortSignal tied to the current scope; pass to fetches so a scope pop
  // cancels them (see abortInFlight above).
  signal: () => scopeController.signal,

  push: (scope: Scope) => commitStackChange({ ...viewState(), stack: [...viewState().stack, scope] }),
  popTo: (i: number) => commitStackChange({ ...viewState(), stack: viewState().stack.slice(0, i + 1) }),
  reset: () => commitStackChange({ ...DEFAULT_STATE }),
  // UF.2: swaps the top-of-stack scope in place — used by the waypoint
  // builder so each chip add/remove live-updates the canvas lane without
  // growing the stack (a `push` per keystroke would wreck breadcrumb/Esc
  // navigation). Aborts in-flight fetches from the scope being replaced,
  // same as push/popTo, since the resolved content is about to change.
  replaceTop: (scope: Scope) => {
    const stack = viewState().stack;
    if (stack.length === 0) return;
    commitStackChange({ ...viewState(), stack: [...stack.slice(0, -1), scope] });
  },

  setIsolation: (iso: FlowRef | undefined) => commit({ ...viewState(), isolation: iso }),
  setFilters: (filters: ViewState["filters"]) => commit({ ...viewState(), filters }),
  setLayout: (layout: string | undefined) => commit({ ...viewState(), layout }),
  setLens: (lens: string) => commit({ ...viewState(), lens }),
  setLensHideUnlinked: (v: boolean) => commit({ ...viewState(), lensHideUnlinked: v }),
  setLensRollup: (v: boolean) => commit({ ...viewState(), lensRollup: v }),
  setCoverageOverlay: (v: boolean) => commit({ ...viewState(), coverageOverlay: v }),
  // UF.7: plain `commit`, never `commitStackChange` — pinning/unpinning must
  // not abort the active scope's in-flight fetch or touch the stack; the
  // canvas fades non-members in place (fade-not-remove) with no refetch.
  setPins: (pins: PinRef[]) => commit({ ...viewState(), pins }),
  // UF.8: commit-expand. Plain `commit`, same reasoning as setPins — growing
  // the expanded set must not abort the active scope's in-flight fetch.
  setExpanded: (expanded: string[]) => commit({ ...viewState(), expanded }),

  // Called by graph loader when a stored node id is no longer valid after reindex
  handleStaleId: () => {
    setStaleIdNotice(true);
    commitStackChange({ ...DEFAULT_STATE });
  },

  // Esc ordering: clear selection → pop isolation → pop scope (bottom of stack = no-op)
  handleEsc: () => {
    if (selectionStore.selection()) {
      selectionStore.setSelection(null);
      return;
    }
    const state = viewState();
    if (state.isolation) {
      commit({ ...state, isolation: undefined });
      return;
    }
    if (state.stack.length > 1) {
      commitStackChange({ ...state, stack: state.stack.slice(0, -1) });
    }
  },
};
