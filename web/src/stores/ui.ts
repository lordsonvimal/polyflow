import { createSignal, createEffect } from "solid-js";
import { DEFAULT_CONFIDENCE } from "../lib/confidence";

export type Layout = "dagre" | "fcose" | "circle" | "grid";
export type ViewMode = "indepth" | "structure" | "highlevel";

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
const initView = (urlParam("view") ?? "indepth") as ViewMode;
// File grouping defaults ON; ?group=off or a persisted preference disables it.
const initGroupByFile = (urlParam("group") ?? lsGet("pf:groupByFile", "files")) !== "off";
// Variables are hidden by default (they crowd the call graph); a persisted
// preference re-enables them. The structure view is exempt — it always shows
// variables since data flow is its whole point (see derived.ts).
const initShowVariables = lsGet("pf:showVariables", "off") === "on";

const [selectedNodeId, setSelectedNodeIdRaw] = createSignal<string | null>(initNodeId);
const [layout, setLayoutRaw] = createSignal<Layout>(initLayout);
const [notification, setNotification] = createSignal<string | null>(null);
const [semanticWarnings, setSemanticWarnings] = createSignal<string[]>([]);

// View mode: in-depth (per-function), structure (classes/variables/flow
// diagram), or high-level (service-to-service).
const [viewMode, setViewModeRaw] = createSignal<ViewMode>(
  initView === "highlevel" || initView === "structure" ? initView : "indepth"
);

// Node types / services the user has hidden (empty = show everything).
const [hiddenTypes, setHiddenTypes] = createSignal<string[]>([]);
const [hiddenServices, setHiddenServices] = createSignal<string[]>([]);

// Confidence levels rendered. Default: static + inferred only — partial and
// unknown edges are opt-in (zero-false-positive default view).
const [activeConfidence, setActiveConfidence] = createSignal<string[]>([...DEFAULT_CONFIDENCE]);

// Boundary groups the user has expanded (collapsed is the default).
const [expandedBoundaries, setExpandedBoundaries] = createSignal<string[]>([]);

// File grouping (in-depth view): nodes wrapped in per-file compound parents.
const [groupByFile, setGroupByFileRaw] = createSignal<boolean>(initGroupByFile);
// File groups the user has collapsed into a single node (expanded default).
const [collapsedFiles, setCollapsedFiles] = createSignal<string[]>([]);

// Variable node visibility (in-depth/high-level). Off by default; structure
// view ignores this and always shows variables.
const [showVariables, setShowVariablesRaw] = createSignal<boolean>(initShowVariables);

// Persist layout to localStorage and URL whenever it changes.
createEffect(() => {
  const l = layout();
  lsSet("pf:layout", l);
  pushURL({ layout: l === "dagre" ? null : l }); // omit default from URL
});

// Persist selected node and view mode to URL.
createEffect(() => {
  pushURL({ node: selectedNodeId() });
});
createEffect(() => {
  const v = viewMode();
  pushURL({ view: v === "indepth" ? null : v });
});

function setSelectedNodeId(id: string | null) {
  setSelectedNodeIdRaw(id);
}

function setLayout(l: Layout) {
  setLayoutRaw(l);
}

function setViewMode(v: ViewMode) {
  setViewModeRaw(v);
  setSelectedNodeIdRaw(null); // selection does not carry across altitudes
}

function toggleHiddenType(type: string) {
  setHiddenTypes((prev) =>
    prev.includes(type) ? prev.filter((t) => t !== type) : [...prev, type]
  );
}

function toggleHiddenService(svc: string) {
  setHiddenServices((prev) =>
    prev.includes(svc) ? prev.filter((s) => s !== svc) : [...prev, svc]
  );
}

function toggleConfidence(level: string) {
  setActiveConfidence((prev) =>
    prev.includes(level) ? prev.filter((c) => c !== level) : [...prev, level]
  );
}

function setGroupByFile(on: boolean) {
  setGroupByFileRaw(on);
  lsSet("pf:groupByFile", on ? "files" : "off");
  pushURL({ group: on ? null : "off" }); // omit default from URL
  if (!on) setCollapsedFiles([]);
}

function setShowVariables(on: boolean) {
  setShowVariablesRaw(on);
  lsSet("pf:showVariables", on ? "on" : "off");
}

function toggleFileCollapse(groupId: string) {
  setCollapsedFiles((prev) =>
    prev.includes(groupId) ? prev.filter((g) => g !== groupId) : [...prev, groupId]
  );
}

function toggleBoundary(groupId: string) {
  setExpandedBoundaries((prev) =>
    prev.includes(groupId) ? prev.filter((g) => g !== groupId) : [...prev, groupId]
  );
}

function expandAllBoundaries(groupIds: string[]) {
  setExpandedBoundaries(groupIds);
}

function collapseAllBoundaries() {
  setExpandedBoundaries([]);
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
  viewMode,
  setViewMode,
  hiddenTypes,
  toggleHiddenType,
  hiddenServices,
  toggleHiddenService,
  activeConfidence,
  toggleConfidence,
  groupByFile,
  setGroupByFile,
  showVariables,
  setShowVariables,
  collapsedFiles,
  toggleFileCollapse,
  expandedBoundaries,
  toggleBoundary,
  expandAllBoundaries,
  collapseAllBoundaries,
  notification,
  setNotification,
  clearNotification,
  semanticWarnings,
  setSemanticWarnings,
  clearSemanticWarnings,
};
