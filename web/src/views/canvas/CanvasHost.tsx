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
import { apiFetch } from "../../lib/apiFetch";
import { EmptyScopeEmptyState } from "../../shell/EmptyState";
import { selectionStore } from "../../stores/selection";
import { canvasElementsStore } from "../../stores/canvasElements";
import { applyFilters } from "../../lib/filters";
import { applyLens, aggregateImportsRollup, DEFAULT_LENS } from "./lenses";
import { importRollupStore } from "../../stores/importRollup";
import FilterBar from "./FilterBar";
import { GraphData, parseCytoGraph, sortGraphData } from "./scopes/common";
import { resolveOverview } from "./scopes/overview";
import { resolveService } from "./scopes/service";
import { resolveFolder } from "./scopes/folder";
import { resolveFile } from "./scopes/file";
import { resolveNeighborhood } from "./scopes/neighborhood";
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
// page" rule without duplicating the list.
export const NO_CANVAS = new Set(["search", "flow", "group"]);

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
    case "impact": {
      const p = new URLSearchParams({ root: scope.target, direction: scope.direction, depth: String(scope.depth) });
      const r = await apiFetch(`/api/graph/trace?${p}`, { signal, silent: true });
      return sortGraphData(parseCytoGraph(await r.json()));
    }
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

  // When scope changes, clear any cluster override.
  const [clusteredData, setClusteredData] = createSignal<GraphData | null>(null);
  createEffect(() => { scope(); setClusteredData(null); });

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

      // Cytoscape sizes its internal <canvas> layers once at construction and
      // never re-reads the container box on its own — without this, resizing
      // the container (e.g. opening the bottom drawer) leaves stale, oversized
      // canvas layers painting over whatever is now below the shrunk wrapper.
      const resizeObserver = typeof ResizeObserver !== "undefined" ? new ResizeObserver(() => cy?.resize()) : undefined;
      resizeObserver?.observe(canvasRef);

      onCleanup(() => { resizeObserver?.disconnect(); unwire(); cy?.destroy(); cy = undefined; });
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

      {/* Placeholder for canvas-free scopes */}
      <Show when={isNoCanvas()}>
        <div class="absolute inset-0 flex items-center justify-center text-neutral-500 text-sm">
          {scope().kind === "search" && "Search & Flow — implemented in plan 11"}
          {(scope().kind === "flow" || scope().kind === "group") && "Flow & Group views — planned in plan 12"}
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
