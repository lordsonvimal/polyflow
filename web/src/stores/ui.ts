import { createSignal, createEffect } from "solid-js";

type Layout = "dagre" | "fcose" | "circle" | "grid";

// ── localStorage helpers ───────────────────────────────────────────────────

function lsGet(key: string, fallback: string): string {
  try { return localStorage.getItem(key) ?? fallback; } catch { return fallback; }
}
function lsSet(key: string, value: string): void {
  try { localStorage.setItem(key, value); } catch { /* ignore quota errors */ }
}

// ── URL helpers ────────────────────────────────────────────────────────────

function urlParam(key: string): string | null {
  return new URLSearchParams(window.location.search).get(key);
}

function pushURL(params: Record<string, string | null>) {
  const sp = new URLSearchParams(window.location.search);
  for (const [k, v] of Object.entries(params)) {
    if (v === null || v === "") sp.delete(k);
    else sp.set(k, v);
  }
  const query = sp.toString();
  window.history.pushState(null, "", query ? `?${query}` : window.location.pathname);
}

// ── Initial values from URL → localStorage fallback → hardcoded defaults ──

const initLayout = (urlParam("layout") ?? lsGet("pf:layout", "dagre")) as Layout;
const initNodeId = urlParam("node");

const [selectedNodeId, setSelectedNodeIdRaw] = createSignal<string | null>(initNodeId);
const [layout, setLayoutRaw] = createSignal<Layout>(initLayout);
const [activeFilters, setActiveFilters] = createSignal<string[]>([]);
const [notification, setNotification] = createSignal<string | null>(null);
const [semanticWarnings, setSemanticWarnings] = createSignal<string[]>([]);

// Persist layout to localStorage and URL whenever it changes.
createEffect(() => {
  const l = layout();
  lsSet("pf:layout", l);
  pushURL({ layout: l === "dagre" ? null : l }); // omit default from URL
});

// Persist selected node to URL whenever it changes.
createEffect(() => {
  const id = selectedNodeId();
  pushURL({ node: id });
});

function setSelectedNodeId(id: string | null) {
  setSelectedNodeIdRaw(id);
}

function setLayout(l: Layout) {
  setLayoutRaw(l);
}

function addFilter(type: string) {
  setActiveFilters((prev) => (prev.includes(type) ? prev : [...prev, type]));
}

function removeFilter(type: string) {
  setActiveFilters((prev) => prev.filter((f) => f !== type));
}

function clearNotification() {
  setNotification(null);
}

function clearSemanticWarnings() {
  setSemanticWarnings([]);
}

export const uiStore = {
  selectedNodeId,
  setSelectedNodeId,
  layout,
  setLayout,
  activeFilters,
  addFilter,
  removeFilter,
  notification,
  setNotification,
  clearNotification,
  semanticWarnings,
  setSemanticWarnings,
  clearSemanticWarnings,
};
