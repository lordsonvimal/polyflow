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
import { GraphNode, GraphEdge } from "../../lib/types";
import { checkBudget, autoCluster, layoutOptions, BUDGET, BudgetOver } from "./budget";
import { wireCytoscape, handleIntent } from "../../interaction/gestures";
import { apiFetch } from "../../lib/apiFetch";
import { EmptyScopeEmptyState } from "../../shell/EmptyState";
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

interface GraphData {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

function parseCytoGraph(raw: unknown): GraphData {
  const r = raw as { nodes?: unknown[]; edges?: unknown[] };
  return {
    nodes: (r.nodes ?? []).map((n: any) => ({
      id: n.data.id,
      type: n.data.type,
      label: n.data.label,
      service: n.data.service ?? "",
      file: n.data.file ?? "",
      line: n.data.line ?? 0,
      language: n.data.language ?? "",
      meta: n.data.meta,
    })),
    edges: (r.edges ?? []).map((e: any) => ({
      id: e.data.id,
      from: e.data.source,
      to: e.data.target,
      type: e.data.type,
      label: e.data.label,
      confidence: e.data.confidence,
      meta: e.data.meta,
    })),
  };
}

async function fetchAll(signal?: AbortSignal): Promise<GraphData> {
  const r = await apiFetch("/api/graph?limit=2000", { signal, silent: true });
  return parseCytoGraph(await r.json());
}

// Scopes that have no canvas — show a placeholder instead.
const NO_CANVAS = new Set(["search", "flow", "group"]);

async function fetchForScope(scope: Scope, signal?: AbortSignal): Promise<GraphData | null> {
  if (NO_CANVAS.has(scope.kind)) return null;

  switch (scope.kind) {
    case "overview":
      return fetchAll(signal);

    case "service": {
      const all = await fetchAll(signal);
      const ids = new Set(all.nodes.filter((n) => n.service === scope.service).map((n) => n.id));
      return {
        nodes: all.nodes.filter((n) => ids.has(n.id)),
        edges: all.edges.filter((e) => ids.has(e.from) && ids.has(e.to)),
      };
    }

    case "folder": {
      const all = await fetchAll(signal);
      const ids = new Set(
        all.nodes.filter((n) => n.service === scope.service && n.file.startsWith(scope.path)).map((n) => n.id),
      );
      return {
        nodes: all.nodes.filter((n) => ids.has(n.id)),
        edges: all.edges.filter((e) => ids.has(e.from) && ids.has(e.to)),
      };
    }

    case "file": {
      const all = await fetchAll(signal);
      const ids = new Set(
        all.nodes
          .filter((n) => n.file === scope.path && (!scope.service || n.service === scope.service))
          .map((n) => n.id),
      );
      return {
        nodes: all.nodes.filter((n) => ids.has(n.id)),
        edges: all.edges.filter((e) => ids.has(e.from) && ids.has(e.to)),
      };
    }

    case "neighborhood": {
      const p = new URLSearchParams({ root: scope.nodeId, direction: "both", depth: String(scope.depth) });
      const r = await apiFetch(`/api/graph/trace?${p}`, { signal, silent: true });
      return parseCytoGraph(await r.json());
    }

    case "impact": {
      const p = new URLSearchParams({ root: scope.target, direction: scope.direction, depth: String(scope.depth) });
      const r = await apiFetch(`/api/graph/trace?${p}`, { signal, silent: true });
      return parseCytoGraph(await r.json());
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

  const budgetResult = createMemo(() => {
    const d = clusteredData() ?? data();
    if (!d) return null;
    return checkBudget(d.nodes, d.edges);
  });

  const renderData = createMemo((): GraphData | null => {
    const d = clusteredData() ?? data();
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

  onMount(() => {
    try {
      cy = cytoscape({
        container: canvasRef,
        style: buildStylesheet(),
        elements: [],
        userZoomingEnabled: true,
        userPanningEnabled: true,
        backgroundColor: CANVAS_BG,
      });
      const unwire = wireCytoscape(cy, handleIntent);
      onCleanup(() => { unwire(); cy?.destroy(); cy = undefined; });
    } catch {
      // Canvas renderer unavailable (e.g., jsdom in tests)
    }
  });

  createEffect(() => {
    const d = renderData();
    if (!cy || !d) return;
    const hasCompounds = d.nodes.some((n) => !!n.parent);
    const opts = layoutOptions(hasCompounds, preferredLayout(), reducedMotion());
    setDagreDisabledReason(opts.dagreDisabledReason);

    const elements = toElements(d);
    const prevCount = cy.elements().length;
    // Morph (animate) for small element counts; fade-swap otherwise.
    const canMorph = opts.animate && prevCount > 0 && (prevCount + elements.length) <= 500;

    if (canMorph) {
      cy.nodes().style("opacity", 0.3);
      cy.elements().remove();
      cy.add(elements);
      const layout = cy.layout({ name: opts.name, animate: true, animationDuration: opts.animationDuration, fit: true, padding: 30 });
      layout.on("layoutstop", () => cy?.nodes().animate({ style: { opacity: 1 } }, { duration: 150 }));
      layout.run();
    } else {
      cy.elements().remove();
      cy.add(elements);
      cy.layout({ name: opts.name, animate: false, fit: true, padding: 30 }).run();
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
    }
  };

  return (
    <div data-testid="canvas-host" class="flex-1 relative min-w-0 flex" style={{ background: CANVAS_BG }}>
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
  );
}
