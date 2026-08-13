import { createSignal } from "solid-js";
import { selectionStore } from "./selection";

// plan 12 defines full FlowRef — forward-declared here
export type FlowRef = { id: string; label?: string };

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
};

export const DEFAULT_STATE: ViewState = {
  stack: [{ kind: "search" }],
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

  setIsolation: (iso: FlowRef | undefined) => commit({ ...viewState(), isolation: iso }),
  setFilters: (filters: ViewState["filters"]) => commit({ ...viewState(), filters }),
  setLayout: (layout: string | undefined) => commit({ ...viewState(), layout }),

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
