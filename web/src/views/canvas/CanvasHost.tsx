import {
  createSignal,
  createMemo,
  createResource,
  createEffect,
  onMount,
  onCleanup,
  Show,
  For,
} from "solid-js";
// @ts-ignore — no bundled types for cytoscape extensions
import cytoscape from "cytoscape";
// @ts-ignore
import fcose from "cytoscape-fcose";
// @ts-ignore
import dagreFn from "cytoscape-dagre";

import { scopeStore, Scope } from "../../stores/scope";
import { checkBudget, autoCluster, layoutOptions, BUDGET, BudgetOver } from "./budget";
import { wireCytoscape, handleIntent, Intent } from "../../interaction/gestures";
import { registerMenuItems, openMenu } from "../../interaction/ContextMenu";
import { EmptyScopeEmptyState } from "../../shell/EmptyState";
import { selectionStore } from "../../stores/selection";
import { canvasElementsStore } from "../../stores/canvasElements";
import { flowHighlightStore } from "../../stores/flowHighlight";
import { flowsThroughStore } from "../../stores/flowsThrough";
import { pathFinderStore } from "../../stores/pathFinder";
import { pathOverlayStore } from "../../stores/pathOverlay";
import { waypointBuilderStore } from "../../stores/waypointBuilder";
import { servicePairStore } from "../../stores/servicePair";
import { multiSelectStore } from "../../stores/multiSelect";
import { pinboardStore } from "../../stores/pinboard";
import { resolvePinboard, filterChainsByLens, pinboardMemberIds } from "./scopes/pinboard";
import { serviceFromNodeId } from "../../lib/aggregate";
import { layoutPrefs } from "../../stores/layoutPrefs";
import { applyFilters } from "../../lib/filters";
import { applyLens, aggregateImportsRollup, DEFAULT_LENS } from "./lenses";
import { applyFileGrouping, FILE_GROUP_TYPE } from "../../lib/filegroup";
import { contextCopyStore } from "../../stores/contextCopy";
import { importRollupStore } from "../../stores/importRollup";
import { treeStore } from "../../stores/tree";
import { drawerStore } from "../../stores/drawer";
import FilterBar from "./FilterBar";
import FlowLane from "../flows/FlowLane";
import { GraphData, sortGraphData } from "./scopes/common";
import { resolveOverview } from "./scopes/overview";
import { resolveService } from "./scopes/service";
import { resolveFolder } from "./scopes/folder";
import { resolveFile } from "./scopes/file";
import { resolveNeighborhood } from "./scopes/neighborhood";
import { resolveGroup } from "./scopes/group";
import { resolveImpact } from "./scopes/impact";
import { stackKey, getViewport, saveViewport } from "./viewportCache";
import {
  NODE_TYPE_STYLES,
  CANVAS_BG,
  LABEL_COLOR,
  DEFAULT_NODE_COLOR,
  LANG_COLORS,
} from "../../lib/styles";

// Register Cytoscape extensions once at module load.
cytoscape.use(fcose);
cytoscape.use(dagreFn);

// Scopes that have no canvas — show a placeholder instead. Exported so
// TopBar can gate the lens control (UN.5) on the same "is this a canvas
// page" rule without duplicating the list. UF.4's group scope DOES render
// on canvas (the induced subgraph, default fcose) — it's a real scope, not
// a placeholder one, so it's deliberately absent here.
export const NO_CANVAS = new Set(["search", "flow"]);

const MENU_ACTIVITY_ID = "canvas";

// UF.6: default ring depth for a freshly-opened "Impact from here" scope —
// ImpactView's stepper (1-10) takes it from there.
const IMPACT_DEFAULT_DEPTH = 3;

// UF.2: "Overlay all" path colors — 5 distinct accents, index 5 is the
// shared "more" bucket for every path beyond the 5th (pathOverlay.ts).
const PATH_OVERLAY_COLORS = ["#f87171", "#fbbf24", "#34d399", "#60a5fa", "#c084fc"];
const PATH_OVERLAY_MORE_COLOR = "#9ca3af";
const PATH_OVERLAY_CLASSES = [
  ...PATH_OVERLAY_COLORS.map((_, i) => `path-overlay-${i}`),
  "path-overlay-more",
];

// UN.1: each drill scope (overview/service/folder/file/neighborhood) has its
// own resolver module under scopes/ — one module per pinned scope kind, all
// returning pre-sorted GraphData (rule 2: deterministic element order).
// Impact stays resolved inline (unchanged from earlier phases; plan-11 does
// not name it among UN.1's pinned resolver files).
async function fetchForScope(scope: Scope, signal?: AbortSignal): Promise<GraphData | null> {
  if (NO_CANVAS.has(scope.kind)) return null;

  switch (scope.kind) {
    case "overview":
      return resolveOverview(signal);
    case "service":
      return resolveService(scope, signal);
    case "folder":
      return resolveFolder(scope, signal);
    case "file":
      return resolveFile(scope, signal);
    case "neighborhood":
      return resolveNeighborhood(scope, signal);
    case "group":
      return resolveGroup(scope, signal);
    case "impact":
      return resolveImpact(scope, signal);
  }
}

function buildStylesheet(): object[] {
  return [
    {
      selector: "node",
      style: {
        "background-color": DEFAULT_NODE_COLOR,
        label: "data(label)",
        "font-size": "10px",
        color: LABEL_COLOR,
        "text-valign": "bottom",
        "text-halign": "center",
        "min-zoomed-font-size": "6px",
        "text-wrap": "ellipsis",
        "text-max-width": "120px",
        width: "30px",
        height: "30px",
      },
    },
    // Language color overrides (before per-type, so per-type fixed colors win)
    ...LANG_COLORS.map(([lang, color]) => ({
      selector: `node[language = "${lang}"]`,
      style: { "background-color": color },
    })),
    // Per-type shape + optional fixed color
    ...NODE_TYPE_STYLES.map((t) => ({
      selector: `node[type = "${t.type}"]`,
      style: { shape: t.shape, ...(t.color ? { "background-color": t.color } : {}) },
    })),
    {
      selector: "edge",
      style: {
        "line-color": "#374151",
        "target-arrow-color": "#374151",
        "target-arrow-shape": "triangle",
        "curve-style": "bezier",
        "font-size": "9px",
        color: "#6b7280",
        width: 1,
      },
    },
    { selector: "edge[confidence = 'candidate']", style: { "line-style": "dashed" } },
    { selector: "edge[confidence = 'conflicting']", style: { "line-style": "dotted" } },
    // Opt-in confidence tiers (FilterBar, UN.2): partial/unknown edges are
    // only ever on canvas because the corresponding chip was explicitly
    // turned on, so they render dashed as a standing reminder they're
    // uncertain — never presented identically to a static/inferred edge.
    { selector: "edge[confidence = 'partial']", style: { "line-style": "dashed" } },
    { selector: "edge[confidence = 'unknown']", style: { "line-style": "dashed" } },
    // UF.6: coverage overlay — plan-10's verification-state edge encoding,
    // now applied everywhere (not just FlowLane's bespoke stylesheet).
    // Takes precedence over the confidence-based rules above (declared
    // after them; Cytoscape's cascade is last-selector-wins per property).
    { selector: "edge[verification_state = 'candidate']", style: { "line-style": "dashed" } },
    { selector: "edge[verification_state = 'conflicting']", style: { "line-style": "dotted" } },
    { selector: "edge[verification_state = 'observed_only_gap']", style: { "line-style": "dashed", width: 3 } },
    // UF.6: impact scope depth rings — target accented, direct dependents
    // strong, transitive fading. Set client-side (scopes/impact.ts's BFS
    // over the already-fetched edge set) since /api/graph/trace has no
    // per-node depth in its response.
    {
      selector: "node[impact_role = 'target']",
      style: { "border-width": 3, "border-color": "#f59e0b" },
    },
    {
      selector: "node[impact_role = 'direct']",
      style: { "border-width": 2, "border-color": "#818cf8", opacity: 1 },
    },
    { selector: "node[impact_role = 'transitive']", style: { opacity: 0.55 } },
    // UF.6: coverage overlay ⚠ badge — a node whose file has unresolved
    // refs (CanvasHost's coverage-overlay effect, computed client-side from
    // treeStore's per-service unresolved ledger). Amber dashed ring, same
    // visual language as the tree's ⚠ badge count.
    {
      selector: "node.coverage-unresolved",
      style: { "border-width": 2, "border-style": "dashed", "border-color": "#f59e0b" },
    },
    // Boundary connectors: a node standing in for something outside the
    // current scope (plan-10's stub-connector contract) — dimmed + dashed
    // border so it reads as "click to expand", not a peer element.
    {
      selector: "node[stub = 'true']",
      style: { "background-opacity": 0.35, "border-width": 1, "border-style": "dashed", "border-color": "#6b7280" },
    },
    // UN.5: nodes with no visible edge under the active lens dim to 30%
    // rather than disappear (lenses.ts's applyLens) — orientation is kept
    // until the user opts into "hide unlinked".
    { selector: "node[lens_dim = 'true']", style: { opacity: 0.3 } },
    // UF.1: ThroughPanel row hover — cheap dim of everything outside the
    // hovered flow's member set, classes only (no layout call).
    { selector: ".flow-highlight-dim", style: { opacity: 0.15 } },
    {
      selector: ".flow-highlight-member",
      style: { opacity: 1, "border-width": 2, "border-color": "#818cf8" },
    },
    // UF.2: path-finder "Overlay all" — one border color per ranked path,
    // shared nodes keep their best-ranked path's color (pathOverlay.ts).
    ...PATH_OVERLAY_COLORS.map((color, i) => ({
      selector: `.path-overlay-${i}`,
      style: { "border-width": 3, "border-color": color },
    })),
    { selector: ".path-overlay-more", style: { "border-width": 3, "border-color": PATH_OVERLAY_MORE_COLOR } },
    // UF.7: pinboard mode (≥2 pins) — same fade-not-remove discipline as the
    // flow-highlight classes above: dim everything not on a path through
    // every pin, never actually remove it (unpinning restores instantly).
    { selector: ".pinboard-dim", style: { opacity: 0.15 } },
    { selector: ".pinboard-member", style: { opacity: 1, "border-width": 2, "border-color": "#2dd4bf" } },
    // UF.7: 📌 badge — any pinned node, independent of pinboard mode (a
    // single pin only badges, per the plan's "1 pin only badges" rule).
    { selector: ".pinboard-pinned", style: { "border-width": 3, "border-color": "#f472b6" } },
    {
      selector: "$node > node",
      style: {
        "background-color": "#111827",
        "background-opacity": 0.7,
        "border-width": 1,
        "border-color": "#374151",
        padding: "10px",
        label: "data(label)",
      },
    },
    {
      selector: ":selected",
      style: { "border-width": 2, "border-color": "#ffffff", "overlay-opacity": 0.1, "overlay-color": "#ffffff" },
    },
  ];
}

function toElements(d: GraphData): object[] {
  return [
    ...d.nodes.map((n) => ({
      group: "nodes",
      data: {
        id: n.id,
        label: n.label,
        type: n.type,
        service: n.service,
        file: n.file,
        line: n.line,
        language: n.language,
        ...(n.parent ? { parent: n.parent } : {}),
        ...(n.meta ?? {}),
      },
    })),
    ...d.edges.map((e) => ({
      group: "edges",
      data: {
        id: e.id,
        source: e.from,
        target: e.to,
        type: e.type,
        ...(e.label ? { label: e.label } : {}),
        ...(e.confidence ? { confidence: e.confidence } : {}),
        ...(e.verificationState ? { verification_state: e.verificationState } : {}),
        ...(e.meta ?? {}),
      },
    })),
  ];
}

export default function CanvasHost() {
  const scope = createMemo(() => scopeStore.stack().at(-1) ?? ({ kind: "search" } as Scope));
  const isNoCanvas = createMemo(() => NO_CANVAS.has(scope().kind));

  const reducedMotion = () =>
    typeof window !== "undefined" && !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

  const [data, { refetch }] = createResource(scope, (s) => fetchForScope(s, scopeStore.signal()));

  const isAbortError = (err: unknown) => err instanceof DOMException && err.name === "AbortError";

  // When scope changes, clear any cluster override and the multi-selection
  // HUD (a stale "N selected" chip surviving a scope change would offer
  // "View as group" over nodes no longer on screen).
  const [clusteredData, setClusteredData] = createSignal<GraphData | null>(null);
  createEffect(() => { scope(); setClusteredData(null); multiSelectStore.clear(); });

  // Lens (UN.5) narrows first — its own axis over edge types, independent
  // of FilterBar's coarser edgeType chips (see lenses.ts's header note) —
  // then Imports' optional module rollup collapses what's left to file→file
  // counts before FilterBar's confidence/edgeType/service chips apply.
  const lensedData = createMemo((): GraphData | null => {
    const d = clusteredData() ?? data();
    if (!d) return null;
    const vs = scopeStore.viewState();
    const lens = vs.lens ?? DEFAULT_LENS;
    const lensed = applyLens(d, lens, { hideUnlinked: !!vs.lensHideUnlinked });
    if (lens === "Imports" && vs.lensRollup) {
      const { nodes, edges, detail } = aggregateImportsRollup(lensed);
      importRollupStore.setDetail(detail);
      return { nodes, edges };
    }
    importRollupStore.setDetail(new Map());
    return lensed;
  });

  // Filters (US.1 ViewState.filters, UN.2 FilterBar) are applied before the
  // budget check so a narrowing filter can pull a scope back under budget,
  // and before render so the element set on screen always matches the chips.
  const filteredData = createMemo((): GraphData | null => {
    const d = lensedData();
    if (!d) return null;
    return applyFilters(d, scopeStore.viewState().filters);
  });

  const budgetResult = createMemo(() => {
    const d = filteredData();
    if (!d) return null;
    return checkBudget(d.nodes, d.edges);
  });

  const renderData = createMemo((): GraphData | null => {
    const d = filteredData();
    const br = budgetResult();
    if (!d || !br || !br.ok) return null;
    return d;
  });

  const budgetOver = createMemo((): BudgetOver | null => {
    if (clusteredData()) return null;
    const br = budgetResult();
    return br && !br.ok ? (br as BudgetOver) : null;
  });

  const [preferredLayout, setPreferredLayout] = createSignal("fcose");
  const [dagreDisabledReason, setDagreDisabledReason] = createSignal<string | undefined>(undefined);

  let canvasRef!: HTMLDivElement;
  let cy: ReturnType<typeof cytoscape> | undefined;

  // Boundary-stub clicks (plan-10's stub-connector contract): a node with
  // meta.stub="true" (flattened onto Cytoscape data by toElements) always
  // resolves to "push the scope that brings it into view" instead of the
  // generic select/drill behavior — never a silent no-op.
  function scopeForStub(el: ReturnType<NonNullable<typeof cy>["getElementById"]>): Scope | null {
    if (el.length === 0 || el.data("stub") !== "true") return null;
    const kind = el.data("stub_kind") as string | undefined;
    const service = (el.data("stub_service") as string) ?? "";
    const path = (el.data("stub_path") as string) ?? "";
    if (kind === "service") return { kind: "service", service };
    if (kind === "folder") return { kind: "folder", service, path };
    if (kind === "file") return { kind: "file", service, path };
    return null;
  }

  // Double-click on a real (non-stub) folder/file/service compound drills
  // into it — the same "expand" action a stub click performs, just for a
  // node already inside the current scope. Plain symbol nodes (function,
  // class, ...) fall through to handleIntent's neighborhood drill.
  function scopeForContainer(el: ReturnType<NonNullable<typeof cy>["getElementById"]>): Scope | null {
    if (el.length === 0) return null;
    const type = el.data("type") as string | undefined;
    const service = (el.data("service") as string) ?? "";
    if (type === "service") return { kind: "service", service };
    if (type === "folder") return { kind: "folder", service, path: (el.data("path") as string) ?? "" };
    if (type === "file") return { kind: "file", service, path: (el.data("file") as string) ?? "" };
    return null;
  }

  function onCanvasIntent(intent: Intent) {
    // UF.4: shift-click on a canvas node already drives Cytoscape's own
    // native additive selection (gestures.ts's onTap fires this alongside
    // it, not instead of it) — the select/unselect listener wired in
    // onMount below mirrors cy's live `:selected` set into
    // multiSelectStore, so forwarding this to the generic handleIntent
    // toggle would flip the same node twice.
    if (intent.type === "multiAdd") return;
    if (intent.type === "menu") {
      const items = [];
      if (intent.target.kind === "node") {
        const nodeId = intent.target.id;
        const label = (cy?.getElementById(nodeId).data("label") as string | undefined) ?? nodeId;
        items.push({
          id: "isolate-flows-through",
          label: "Isolate flows through here",
          handler: () => {
            selectionStore.setSelection({ kind: "node", id: nodeId });
            flowsThroughStore.request(nodeId);
          },
        });
        items.push({
          id: "set-path-start",
          label: "Set as path start",
          handler: () => pathFinderStore.setStart({ id: nodeId, label }),
        });
        // UF.2: "Find paths from A" only makes sense once a start pin
        // exists and this node isn't it — same one-click discipline as
        // UF.1's flows-through action.
        const start = pathFinderStore.startNode();
        if (start && start.id !== nodeId) {
          items.push({
            id: "find-paths-from-a",
            label: `Find paths from ${start.label}`,
            handler: () => {
              selectionStore.setSelection({ kind: "node", id: nodeId });
              pathFinderStore.requestPaths({ id: nodeId, label });
            },
          });
        }
        items.push({
          id: "start-flow-here",
          label: "Start flow here",
          handler: () => {
            waypointBuilderStore.requestStart({ id: nodeId, label });
            layoutPrefs.setActivity("flows");
          },
        });
        // UF.6: pushes the impact scope (depth rings rendered by
        // scopes/impact.ts) and opens the Impact activity's direction/depth
        // controls in the same click.
        items.push({
          id: "impact-from-here",
          label: "Impact from here",
          handler: () => {
            selectionStore.setSelection({ kind: "node", id: nodeId });
            scopeStore.push({ kind: "impact", target: nodeId, direction: "both", depth: IMPACT_DEFAULT_DEPTH });
            layoutPrefs.setActivity("impact");
          },
        });
        // UF.7: toggles this node's pinboard chip — distinct from
        // selectionStore's existing "Pin to compare" (📌 pin) action, a
        // different, already-shipped feature this must not collide with.
        items.push({
          id: "pin-to-pinboard",
          label: pinboardStore.isPinned(nodeId) ? "Unpin from pinboard" : "Pin to pinboard",
          handler: () => pinboardStore.toggle({ id: nodeId, label }),
        });
        // UF.6: coverage overlay's ⚠ badge is a border style, not a
        // clickable DOM element (Cytoscape draws to canvas) — the menu is
        // the click-through to the pre-filtered Unresolved drawer tab.
        const el = cy?.getElementById(nodeId);
        const nodeService = (el?.data("service") as string | undefined) ?? "";
        const nodeFile = (el?.data("file") as string | undefined) ?? "";
        if (nodeFile && unresolvedFileSet().has(`${nodeService} ${nodeFile}`)) {
          items.push({
            id: "view-unresolved-for-file",
            label: "⚠ View unresolved refs for this file",
            handler: () => drawerStore.openUnresolvedFor(nodeService, nodeFile),
          });
        }
      }
      // UF.3: seam isolation on any REAL edge — not the synthetic overview
      // aggregation pill (`agg:`, lib/aggregate.ts) or the Imports lens
      // rollup (`rollup:`, lenses.ts), neither of which /api/seam can
      // resolve (there is no one channel to isolate; those get the
      // service-pair drill-in / rollup detail instead).
      if (intent.target.kind === "edge" && !intent.target.id.startsWith("agg:") && !intent.target.id.startsWith("rollup:")) {
        const edgeId = intent.target.id;
        items.push({
          id: "isolate-seam",
          label: "Isolate seam",
          handler: () => {
            selectionStore.setSelection({ kind: "edge", id: edgeId });
            scopeStore.push({ kind: "flow", flow: { kind: "seam", edgeId } });
          },
        });
      }
      registerMenuItems(MENU_ACTIVITY_ID, items);
      openMenu(intent.x, intent.y);
      return;
    }
    if ((intent.type === "select" || intent.type === "drill") && intent.target.kind === "node" && cy) {
      const el = cy.getElementById(intent.target.id);
      const stubScope = scopeForStub(el);
      if (stubScope) {
        scopeStore.push(stubScope);
        return;
      }
      if (intent.type === "drill") {
        const containerScope = scopeForContainer(el);
        if (containerScope) {
          scopeStore.push(containerScope);
          return;
        }
      }
    }
    // UF.3: single-click (or double-click) on an aggregated overview
    // service-pair pill opens the channel-list drill-in instead of the
    // generic edge-select/neighborhood-drill path — there's no single node
    // an `agg:` id resolves to, so handleIntent's default "drill" case
    // (which treats any target id as a node id) would be a dead end here.
    if ((intent.type === "select" || intent.type === "drill") && intent.target.kind === "edge" && cy) {
      const edgeId = intent.target.id;
      if (scope().kind === "overview" && edgeId.startsWith("agg:")) {
        const el = cy.getElementById(edgeId);
        const from = serviceFromNodeId((el.data("source") as string) ?? "");
        const to = serviceFromNodeId((el.data("target") as string) ?? "");
        selectionStore.setSelection({ kind: "edge", id: edgeId });
        servicePairStore.open(from, to, edgeId);
        return;
      }
    }
    handleIntent(intent);
  }

  onMount(() => {
    try {
      cy = cytoscape({
        container: canvasRef,
        style: buildStylesheet(),
        elements: [],
        userZoomingEnabled: true,
        userPanningEnabled: true,
        backgroundColor: CANVAS_BG,
        // Unbounded fit-to-content zoom blows sparse scopes (e.g. a 2-node
        // overview) up to fill the viewport, rendering comically huge nodes.
        minZoom: 0.05,
        maxZoom: 2.5,
      });
      const unwire = wireCytoscape(cy, onCanvasIntent);

      // UF.4: marquee-drag (shift + drag on background, Cytoscape's default
      // box-selection gesture) never fires a "tap" event per node, so it
      // never reaches gestures.ts's intent pipeline — this listener is the
      // only way to observe it. It also covers shift-click's native
      // additive select (see onCanvasIntent's multiAdd intercept above),
      // so it's the single source of truth for what's multi-selected on
      // canvas: always a full mirror of cy's own `:selected` node set.
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const syncMultiSelect = () => multiSelectStore.setIds(new Set(cy!.nodes(":selected").map((n: any) => n.id())));
      cy.on("select", "node", syncMultiSelect);
      cy.on("unselect", "node", syncMultiSelect);

      // Cytoscape sizes its internal <canvas> layers once at construction and
      // never re-reads the container box on its own — without this, resizing
      // the container (e.g. opening the bottom drawer) leaves stale, oversized
      // canvas layers painting over whatever is now below the shrunk wrapper.
      const resizeObserver = typeof ResizeObserver !== "undefined" ? new ResizeObserver(() => cy?.resize()) : undefined;
      resizeObserver?.observe(canvasRef);

      onCleanup(() => {
        resizeObserver?.disconnect();
        cy?.off("select", "node", syncMultiSelect);
        cy?.off("unselect", "node", syncMultiSelect);
        unwire();
        cy?.destroy();
        cy = undefined;
      });
    } catch {
      // Canvas renderer unavailable (e.g., jsdom in tests)
    }
  });

  // Viewport cache: remember the current scope stack's pan/zoom the instant
  // before the stack changes, so popping back to it restores the view
  // instead of re-fitting to content.
  let lastStackKey: string | undefined;
  createEffect(() => {
    const key = stackKey(scopeStore.stack());
    if (lastStackKey !== undefined && lastStackKey !== key && cy) {
      saveViewport(lastStackKey, { pan: cy.pan(), zoom: cy.zoom() });
    }
    lastStackKey = key;
  });

  // Publish the active scope's rendered node ids so other views (e.g. the
  // tree explorer's two-way sync) can tell "on canvas" from "needs a scope
  // change" without reaching into Cytoscape directly.
  createEffect(() => {
    const d = renderData();
    canvasElementsStore.setIds(new Set(d ? d.nodes.map((n) => n.id) : []));

    // UF.5: when budget-forced clustering is active, resolve each rendered
    // `filegrp:` id back to its real member ids (against the *unclustered*
    // data — the group id is deterministic per (service, file), so it's
    // found there regardless of collapse state) so "Copy context" on a
    // scope can expand clusters instead of sending an unresolvable id.
    const clusterMap = new Map<string, string[]>();
    const raw = data();
    if (d && raw) {
      const { groups } = applyFileGrouping(raw.nodes, raw.edges, []);
      const groupMembers = new Map(groups.map((g) => [g.id, g.members.map((m) => m.id)]));
      for (const n of d.nodes) {
        if (n.type === FILE_GROUP_TYPE && n.meta?.collapsed === "true") {
          const members = groupMembers.get(n.id);
          if (members) clusterMap.set(n.id, members);
        }
      }
    }
    canvasElementsStore.setClusters(clusterMap);
  });

  // Reflect external selection changes (e.g. a tree row click) onto the
  // canvas so the two are never visually out of sync.
  createEffect(() => {
    const sel = selectionStore.selection();
    if (!cy) return;
    cy.elements(":selected").unselect();
    if (sel && cy.getElementById(sel.id).length > 0) {
      cy.getElementById(sel.id).select();
    }
  });

  // UF.1: reflect the hovered flow-through group onto the canvas — classes
  // only, no layout call, so it's cheap enough for onMouseEnter/Leave.
  createEffect(() => {
    if (!cy) return;
    const hl = flowHighlightStore.ids();
    cy.batch(() => {
      cy!.elements().removeClass("flow-highlight-dim flow-highlight-member");
      if (hl.size === 0) return;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      cy!.elements().forEach((el: any) => {
        el.addClass(hl.has(el.id()) ? "flow-highlight-member" : "flow-highlight-dim");
      });
    });
  });

  // UF.2: "Overlay all" path colors — same classes-only discipline as the
  // flow-highlight effect above, keyed by pathOverlayStore's node→color map.
  createEffect(() => {
    if (!cy) return;
    const assignment = pathOverlayStore.assignment();
    cy.batch(() => {
      cy!.nodes().removeClass(PATH_OVERLAY_CLASSES.join(" "));
      if (assignment.size === 0) return;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      cy!.nodes().forEach((el: any) => {
        const color = assignment.get(el.id());
        if (color !== undefined) el.addClass(PATH_OVERLAY_CLASSES[color]);
      });
    });
  });

  // UF.7: 📌 badge — any pinned node currently on canvas, independent of
  // pinboard mode (badges from the first pin; the fade below only starts at
  // the second). Classes only, no layout call.
  createEffect(() => {
    if (!cy) return;
    const pinned = new Set(pinboardStore.pins().map((p) => p.id));
    cy.batch(() => {
      cy!.nodes().removeClass("pinboard-pinned");
      if (pinned.size === 0) return;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      cy!.nodes().forEach((el: any) => {
        if (pinned.has(el.id())) el.addClass("pinboard-pinned");
      });
    });
  });

  // UF.7: pinboard mode — only resolves (and only fetches) once 2+ pins are
  // set, mirroring WaypointBuilder/PathFinderPanel's "null key skips the
  // fetch" discipline. Dropping back below 2 pins clears the fade with no
  // network call at all — the "unpinning restores instantly" guarantee.
  const [pinboardResolution] = createResource(
    () => (pinboardStore.active() ? pinboardStore.pins().map((p) => p.id) : null),
    (ids) => resolvePinboard(ids, scopeStore.signal()),
  );

  const pinboardMembers = createMemo((): Set<string> | null => {
    const res = pinboardResolution();
    if (!res) return null;
    const lens = scopeStore.viewState().lens ?? DEFAULT_LENS;
    return pinboardMemberIds(filterChainsByLens(res.chains, lens));
  });

  createEffect(() => {
    if (!cy) return;
    const members = pinboardMembers();
    cy.batch(() => {
      cy!.elements().removeClass("pinboard-dim pinboard-member");
      if (!members) return;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      cy!.nodes().forEach((el: any) => {
        el.addClass(members.has(el.id()) ? "pinboard-member" : "pinboard-dim");
      });
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      cy!.edges().forEach((el: any) => {
        const onPath = members.has(el.data("source")) && members.has(el.data("target"));
        el.addClass(onPath ? "pinboard-member" : "pinboard-dim");
      });
    });
  });

  // UF.6: coverage overlay — ⚠ ledger badge. Loads (lazily, via treeStore's
  // own cache) the unresolved-ref ledger for every service currently on
  // canvas, then flags nodes whose file has at least one unresolved ref.
  // Classes only (no layout call), and skipped entirely when the FilterBar
  // toggle is off.
  createEffect(() => {
    const d = renderData();
    if (!d) return;
    const services = new Set(d.nodes.map((n) => n.service).filter(Boolean));
    for (const svc of services) treeStore.loadService(svc);
  });

  const unresolvedFileSet = createMemo((): Set<string> => {
    const d = renderData();
    if (!d) return new Set();
    const services = new Set(d.nodes.map((n) => n.service).filter(Boolean));
    const files = new Set<string>();
    for (const svc of services) {
      for (const ref of treeStore.entryFor(svc).unresolved ?? []) {
        files.add(`${svc} ${ref.file}`);
      }
    }
    return files;
  });

  createEffect(() => {
    if (!cy) return;
    const d = renderData();
    const overlayOn = scopeStore.viewState().coverageOverlay !== false;
    const files = unresolvedFileSet();
    cy.batch(() => {
      cy!.nodes().removeClass("coverage-unresolved");
      if (!overlayOn || !d) return;
      for (const n of d.nodes) {
        if (n.file && files.has(`${n.service} ${n.file}`)) {
          cy!.getElementById(n.id).addClass("coverage-unresolved");
        }
      }
    });
  });

  // Ids currently on canvas, tracked outside Solid's reactivity so the render
  // effect below can tell "filter narrowed the same scope" (a pure removal)
  // from "scope changed" without re-deriving it from Cytoscape each time.
  let lastElementIds: Set<string> | undefined;
  let lastRenderStackKey: string | undefined;

  createEffect(() => {
    const d = renderData();
    if (!cy || !d) return;

    const nextIds = new Set<string>([...d.nodes.map((n) => n.id), ...d.edges.map((e) => e.id)]);
    const currentStackKey = stackKey(scopeStore.stack());
    const sameScope = lastRenderStackKey === currentStackKey;
    lastRenderStackKey = currentStackKey;

    // Filter chips narrowing the current scope only ever remove elements
    // (never add ones the unfiltered fetch didn't already contain) — detect
    // that case and fade the removed elements out in place, skipping layout
    // entirely ("never re-layout unless element set changed" / no
    // gratuitous motion for a filter-only change). Gated on the scope stack
    // being unchanged so a coincidental subset relationship across two
    // different scopes never skips a real layout.
    if (sameScope && lastElementIds && nextIds.size < lastElementIds.size) {
      let isPureRemoval = true;
      for (const id of nextIds) {
        if (!lastElementIds.has(id)) { isPureRemoval = false; break; }
      }
      if (isPureRemoval) {
        const removed = cy.elements().filter((el) => !nextIds.has(el.id()));
        if (!reducedMotion()) {
          removed.animate({ style: { opacity: 0 } }, { duration: 150, complete: () => removed.remove() });
        } else {
          removed.remove();
        }
        lastElementIds = nextIds;
        return;
      }
    }
    lastElementIds = nextIds;

    const hasCompounds = d.nodes.some((n) => !!n.parent);
    const opts = layoutOptions(hasCompounds, preferredLayout(), reducedMotion());
    setDagreDisabledReason(opts.dagreDisabledReason);

    const elements = toElements(d);
    const prevCount = cy.elements().length;
    // Morph (animate) for small element counts; fade-swap otherwise.
    const canMorph = opts.animate && prevCount > 0 && (prevCount + elements.length) <= 500;

    // A remembered viewport for this exact scope stack (set when the stack
    // last left it) skips the fit-to-content layout in favor of restoring
    // the previous pan/zoom.
    const cached = getViewport(stackKey(scopeStore.stack()));
    const restoreViewport = () => {
      if (cached && cy) {
        cy.pan(cached.pan);
        cy.zoom(cached.zoom);
      }
    };

    if (canMorph) {
      cy.nodes().style("opacity", 0.3);
      cy.elements().remove();
      cy.add(elements);
      const layout = cy.layout({ name: opts.name, animate: true, animationDuration: opts.animationDuration, fit: !cached, padding: 30 });
      layout.on("layoutstop", () => {
        cy?.nodes().animate({ style: { opacity: 1 } }, { duration: 150 });
        restoreViewport();
      });
      layout.run();
    } else {
      cy.elements().remove();
      cy.add(elements);
      cy.layout({ name: opts.name, animate: false, fit: !cached, padding: 30 }).run();
      restoreViewport();
    }
  });

  const handleAutoCluster = () => {
    const d = data();
    if (!d) return;
    setClusteredData(autoCluster(d.nodes, d.edges));
  };

  // UF.4: the HUD chip's "View as group" action — clearing the live
  // Cytoscape selection (rather than leaving it painted) avoids landing on
  // the new group scope with a stale ":selected" set from the scope just
  // left, which the selection-sync effect above would otherwise fight.
  const viewAsGroup = () => {
    const ids = [...multiSelectStore.ids()].sort();
    if (ids.length < 2) return;
    cy?.elements().unselect();
    multiSelectStore.clear();
    scopeStore.push({ kind: "group", nodeIds: ids });
  };

  const clearMultiSelect = () => {
    cy?.elements().unselect();
    multiSelectStore.clear();
  };

  const handleNarrow = (childKey: string) => {
    const s = scope();
    if (s.kind === "overview") {
      scopeStore.push({ kind: "service", service: childKey });
    } else if (s.kind === "service") {
      scopeStore.push({ kind: "folder", service: s.service, path: childKey });
    } else if (s.kind === "folder") {
      scopeStore.push({ kind: "file", service: s.service, path: childKey });
    }
  };

  return (
    <div class="flex-1 min-w-0 flex flex-col">
      <Show when={!isNoCanvas()}>
        <FilterBar />
      </Show>
      <div data-testid="canvas-host" class="flex-1 relative min-w-0 flex overflow-hidden" style={{ background: CANVAS_BG }}>
      {/* Cytoscape container — always mounted so cy instance survives scope changes */}
      <div
        ref={canvasRef!}
        class="w-full h-full"
        classList={{ invisible: isNoCanvas() || !!budgetOver() }}
      />

      {/* UF.0: the flow scope gets its own purpose-built lane renderer,
          not the generic budget/lens/filter canvas pipeline. */}
      <Show when={scope().kind === "flow"}>
        <FlowLane />
      </Show>

      {/* Placeholder for the remaining canvas-free scopes */}
      <Show when={isNoCanvas() && scope().kind !== "flow"}>
        <div class="absolute inset-0 flex items-center justify-center text-neutral-500 text-sm">
          {scope().kind === "search" && "Search & Flow — implemented in plan 11"}
        </div>
      </Show>

      {/* Shimmer while loading */}
      <Show when={data.loading && !isNoCanvas()}>
        <div class="absolute inset-0 flex items-center justify-center bg-neutral-900/60 pointer-events-none">
          <div class="flex gap-2">
            <For each={[0, 1, 2]}>
              {(i) => (
                <div
                  class="w-3 h-3 rounded-full bg-neutral-600 animate-pulse"
                  style={{ "animation-delay": `${i * 150}ms` }}
                />
              )}
            </For>
          </div>
        </div>
      </Show>

      {/* Fetch error (a deliberate scope-pop abort is not a user-facing error) */}
      <Show when={data.error && !isAbortError(data.error) && !isNoCanvas()}>
        <div class="absolute inset-0 flex flex-col items-center justify-center gap-3 text-neutral-400">
          <span class="text-sm">
            Failed to load graph: {String((data.error as Error)?.message ?? data.error)}
          </span>
          <button
            class="px-3 py-1 rounded bg-neutral-700 hover:bg-neutral-600 text-white text-xs"
            onClick={refetch}
          >
            Retry
          </button>
        </div>
      </Show>

      {/* Empty scope — resolved successfully but has nothing to show */}
      <Show when={!data.loading && !data.error && !isNoCanvas() && data() && data()!.nodes.length === 0}>
        <div class="absolute inset-0 flex items-center justify-center">
          <EmptyScopeEmptyState onReset={() => scopeStore.reset()} />
        </div>
      </Show>

      {/* Over-budget dialog */}
      <Show when={budgetOver()}>
        {(ob) => (
          <div class="absolute inset-0 flex items-center justify-center bg-neutral-900/90 z-20">
            <div class="bg-neutral-800 border border-neutral-700 rounded-lg p-6 max-w-md w-full mx-4 shadow-xl">
              <h2 class="text-white font-semibold mb-2">Scope too large</h2>
              <p class="text-neutral-300 text-sm mb-4">
                This scope is{" "}
                <strong class="text-white">{ob().total.toLocaleString()} elements</strong>{" "}
                ({ob().nodeCount.toLocaleString()} nodes + {ob().edgeCount.toLocaleString()} edges).
                The budget is {BUDGET.toLocaleString()}.
              </p>
              <Show when={ob().children.length > 0}>
                <p class="text-neutral-400 text-xs uppercase tracking-wide mb-2">Narrow to</p>
                <div class="flex flex-col gap-1 max-h-40 overflow-y-auto mb-4">
                  <For each={ob().children}>
                    {(child) => (
                      <button
                        class="flex items-center justify-between px-3 py-1.5 rounded bg-neutral-700 hover:bg-neutral-600 text-left text-sm text-white"
                        onClick={() => handleNarrow(child.key)}
                      >
                        <span>{child.label}</span>
                        <span class="text-neutral-400 text-xs">{child.count.toLocaleString()} nodes</span>
                      </button>
                    )}
                  </For>
                </div>
              </Show>
              <div class="flex gap-2">
                <button
                  class="flex-1 px-3 py-1.5 rounded bg-indigo-600 hover:bg-indigo-500 text-white text-sm"
                  onClick={handleAutoCluster}
                >
                  Auto-cluster to folders
                </button>
                <button
                  class="px-3 py-1.5 rounded bg-neutral-700 hover:bg-neutral-600 text-white text-sm"
                  onClick={() => scopeStore.reset()}
                >
                  Cancel
                </button>
              </div>
            </div>
          </div>
        )}
      </Show>

      {/* UF.4: multi-select HUD chip — top-left, appears once the marquee
          drag or shift-click set (mirrored from Cytoscape's own `:selected`
          nodes, or from Tree's shift-click via multiSelectStore.toggle)
          holds 2+ nodes. */}
      <Show when={!isNoCanvas() && multiSelectStore.ids().size >= 2}>
        <div
          data-testid="multiselect-hud"
          class="absolute top-2 left-2 z-10 flex items-center gap-2 px-2 py-1 rounded bg-neutral-800 border border-neutral-700 text-xs text-neutral-200"
        >
          <span>{multiSelectStore.ids().size} selected</span>
          <button
            data-testid="multiselect-view-as-group"
            class="text-indigo-300 hover:text-indigo-200"
            onClick={viewAsGroup}
          >
            View as group
          </button>
          <button
            data-testid="multiselect-copy-context"
            class="text-blue-300 hover:text-blue-200"
            onClick={() => contextCopyStore.copy({ kind: "group", ids: [...multiSelectStore.ids()].sort() })}
          >
            ⧉ copy context
          </button>
          <button
            data-testid="multiselect-clear"
            class="text-neutral-500 hover:text-white"
            onClick={clearMultiSelect}
          >
            × clear
          </button>
        </div>
      </Show>

      {/* UF.7: honest empty pinboard result — "No flow passes through all N
          pins" naming the broken pair, never a silent blank canvas. */}
      <Show when={!isNoCanvas() && pinboardStore.active() && pinboardResolution() && !pinboardResolution()!.reachable}>
        {(res) => {
          const labelFor = (id: string) => pinboardStore.pins().find((p) => p.id === id)?.label ?? id;
          const broken = res().brokenPair;
          return (
            <div
              data-testid="pinboard-empty"
              class="absolute bottom-2 left-2 z-10 flex items-center gap-2 px-3 py-1.5 rounded bg-neutral-800 border border-neutral-700 text-xs text-neutral-300"
            >
              <span>
                No flow passes through all {pinboardStore.pins().length} pins
                <Show when={broken}>
                  {(b) => <> — {labelFor(b().from)} ↮ {labelFor(b().to)}</>}
                </Show>
              </span>
              <Show when={broken}>
                {(b) => (
                  <>
                    <button
                      data-testid="pinboard-remove-from"
                      class="text-indigo-300 hover:text-indigo-200"
                      onClick={() => pinboardStore.unpin(b().from)}
                    >
                      remove {labelFor(b().from)}
                    </button>
                    <button
                      data-testid="pinboard-remove-to"
                      class="text-indigo-300 hover:text-indigo-200"
                      onClick={() => pinboardStore.unpin(b().to)}
                    >
                      remove {labelFor(b().to)}
                    </button>
                  </>
                )}
              </Show>
            </div>
          );
        }}
      </Show>

      {/* Layout picker — top-right of canvas */}
      <Show when={!isNoCanvas()}>
        <div class="absolute top-2 right-2 z-10 flex items-center gap-1 text-xs">
          <button
            class={`px-2 py-0.5 rounded transition-colors ${preferredLayout() === "fcose" ? "bg-neutral-700 text-white" : "text-neutral-500 hover:text-white"}`}
            onClick={() => setPreferredLayout("fcose")}
          >
            fcose
          </button>
          <button
            title={dagreDisabledReason()}
            class={`px-2 py-0.5 rounded transition-colors ${preferredLayout() === "dagre" && !dagreDisabledReason() ? "bg-neutral-700 text-white" : "text-neutral-500 hover:text-white"} ${dagreDisabledReason() ? "opacity-40 cursor-not-allowed line-through" : ""}`}
            onClick={() => { if (!dagreDisabledReason()) setPreferredLayout("dagre"); }}
          >
            dagre
          </button>
          <Show when={dagreDisabledReason()}>
            <span class="text-amber-400 text-xs">{dagreDisabledReason()}</span>
          </Show>
        </div>
      </Show>
      </div>
    </div>
  );
}
