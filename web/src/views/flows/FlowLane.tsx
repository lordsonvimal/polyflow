// UF.0: purpose-built reading layout for an isolated flow — left→right by
// hop order, one horizontal swimlane per service — as opposed to the
// generic scope canvas (CanvasHost.tsx) every other scope kind uses.
import { createResource, createMemo, createSignal, createEffect, onMount, onCleanup, Show, For } from "solid-js";
// @ts-ignore — no bundled types for cytoscape
import cytoscape from "cytoscape";

import { scopeStore, Scope } from "../../stores/scope";
import { resolveFlow, computeFlowLaneLayout, FlowResolution, SEAM_CHANNEL_PREFIX } from "../canvas/scopes/flow";
import { wireCytoscape, handleIntent, Intent } from "../../interaction/gestures";
import { selectionStore } from "../../stores/selection";
import { CANVAS_BG, LABEL_COLOR, DEFAULT_NODE_COLOR, NODE_TYPE_STYLES, LANG_COLORS } from "../../lib/styles";
import { contextCopyStore } from "../../stores/contextCopy";
import { flowRefToSource } from "../context/copy";
import { displayLabel } from "../../lib/location";

const LANE_HEIGHT = 140;
const HOP_SPACING = 220;
const LANE_LEFT_PAD = 160;

function reducedMotion(): boolean {
  return typeof window !== "undefined" && !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
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
        "text-wrap": "ellipsis",
        "text-max-width": "140px",
        width: "28px",
        height: "28px",
      },
    },
    ...LANG_COLORS.map(([lang, color]) => ({
      selector: `node[language = "${lang}"]`,
      style: { "background-color": color },
    })),
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
    // plan-10's verification-state edge encoding: solid verified / dashed
    // candidate / dotted conflicting / observed_only_gap. Cytoscape has no
    // native "double line" style, so observed_only_gap is approximated with
    // an extra-wide dashed line rather than left indistinguishable from
    // candidate.
    { selector: "edge[verification_state = 'candidate']", style: { "line-style": "dashed" } },
    { selector: "edge[verification_state = 'conflicting']", style: { "line-style": "dotted" } },
    { selector: "edge[verification_state = 'observed_only_gap']", style: { "line-style": "dashed", width: 3 } },
    // Cross-service hops: a channel-key pill on the lane-crossing edge.
    {
      selector: "edge[cross_service = 'true']",
      style: {
        label: "data(edgeLabel)",
        "text-background-color": "#1f2937",
        "text-background-opacity": 1,
        "text-background-padding": "3px",
        "text-border-width": 1,
        "text-border-color": "#4b5563",
        "line-color": "#818cf8",
        "target-arrow-color": "#818cf8",
        width: 1.5,
      },
    },
    {
      selector: ":selected",
      style: { "border-width": 2, "border-color": "#ffffff", "overlay-opacity": 0.1, "overlay-color": "#ffffff" },
    },
    // UF.3: the synthetic seam channel node — a pill, not a symbol node, so
    // "producers left, channel center, consumers right" reads at a glance.
    {
      selector: `node[id ^= "${SEAM_CHANNEL_PREFIX}"]`,
      style: {
        shape: "round-rectangle",
        "background-color": "#4338ca",
        width: "label",
        height: "24px",
        padding: "6px",
        "text-valign": "center",
        "text-halign": "center",
        "font-size": "11px",
        color: "#ffffff",
      },
    },
  ];
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function toElements(res: FlowResolution): any[] {
  const layout = computeFlowLaneLayout(res.chains);

  const nodes = layout.nodes.map((n) => ({
    group: "nodes",
    data: { id: n.id, label: displayLabel(n.label), service: n.service, verification_state: n.verificationState },
    position: { x: LANE_LEFT_PAD + n.rank * HOP_SPACING, y: n.lane * LANE_HEIGHT + LANE_HEIGHT / 2 },
  }));

  const edges = layout.edges.map((e) => ({
    group: "edges",
    data: {
      id: e.id,
      source: e.from,
      target: e.to,
      edgeType: e.edgeType,
      edgeLabel: e.edgeLabel,
      cross_service: e.crossService ? "true" : "false",
      confidence: e.confidence,
      verification_state: e.verificationState,
    },
  }));

  return [...nodes, ...edges];
}

export default function FlowLane() {
  const scope = createMemo(() => scopeStore.stack().at(-1) as Extract<Scope, { kind: "flow" }> | undefined);
  const [throughLimit, setThroughLimit] = createSignal(20);

  // Reset the depth override whenever the flow ref itself changes (not on a
  // limit-only refetch) so switching to a different flow starts fresh.
  createEffect(() => { scope()?.flow; setThroughLimit(20); });

  const [resolution, { refetch }] = createResource(
    () => (scope() ? ({ flow: scope()!.flow, limit: throughLimit() }) : null),
    (args) => resolveFlow(args!.flow, scopeStore.signal(), { throughLimit: args!.limit }),
  );

  const isAbortError = (err: unknown) => err instanceof DOMException && err.name === "AbortError";

  let canvasRef!: HTMLDivElement;
  let cy: ReturnType<typeof cytoscape> | undefined;
  const [viewport, setViewport] = createSignal({ pan: { x: 0, y: 0 }, zoom: 1 });

  function onIntent(intent: Intent) {
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
        minZoom: 0.2,
        maxZoom: 2.5,
      });
      const unwire = wireCytoscape(cy, onIntent);
      const syncViewport = () => cy && setViewport({ pan: cy.pan(), zoom: cy.zoom() });
      cy.on("pan zoom", syncViewport);
      onCleanup(() => { cy?.off("pan zoom", syncViewport); unwire(); cy?.destroy(); cy = undefined; });
    } catch {
      // Canvas renderer unavailable (e.g., jsdom in tests)
    }
  });

  // Reflect external selection changes onto the canvas.
  createEffect(() => {
    const sel = selectionStore.selection();
    if (!cy) return;
    cy.elements(":selected").unselect();
    if (sel && cy.getElementById(sel.id).length > 0) cy.getElementById(sel.id).select();
  });

  createEffect(() => {
    const res = resolution();
    if (!cy || !res) return;
    cy.elements().remove();
    cy.add(toElements(res));
    if (!reducedMotion()) {
      cy.nodes().style("opacity", 0);
      cy.nodes().animate({ style: { opacity: 1 } }, { duration: 200 });
    }
    cy.fit(undefined, 40);
    setViewport({ pan: cy.pan(), zoom: cy.zoom() });
  });

  const laneRows = createMemo(() => {
    const res = resolution();
    if (!res) return [];
    return computeFlowLaneLayout(res.chains).services;
  });

  const closeFlow = () => {
    const stack = scopeStore.stack();
    scopeStore.popTo(Math.max(0, stack.length - 2));
  };

  return (
    <div class="absolute inset-0 flex flex-col" style={{ background: CANVAS_BG }}>
      <div class="absolute top-2 left-2 z-10 flex items-center gap-2 bg-neutral-800/90 border border-neutral-700 rounded px-3 py-1.5 text-sm text-white">
        <span>Flow: {resolution()?.label ?? "…"}</span>
        <Show when={scope()}>
          {(s) => (
            <button
              data-testid="flow-chip-copy-context"
              class="text-blue-300 hover:text-blue-200 text-xs"
              title="Copy context"
              onClick={() => contextCopyStore.copy(flowRefToSource(s().flow, resolution()?.chains ?? []))}
            >
              ⧉ copy context
            </button>
          )}
        </Show>
        <button class="text-neutral-400 hover:text-white" onClick={closeFlow} title="Close flow (Esc)">
          ×
        </button>
      </div>

      {/* Lane labels pinned to the left edge, tracking the canvas pan/zoom. */}
      <For each={laneRows()}>
        {(service, i) => (
          <div
            class="absolute left-2 z-10 px-2 py-1 text-xs text-neutral-400 bg-neutral-900/70 rounded pointer-events-none truncate max-w-[140px]"
            style={{
              top: `${viewport().pan.y + i() * LANE_HEIGHT * viewport().zoom + (LANE_HEIGHT / 2) * viewport().zoom - 10}px`,
            }}
          >
            {service}
          </div>
        )}
      </For>

      <div ref={canvasRef!} class="w-full h-full" />

      <Show when={resolution.loading}>
        <div class="absolute inset-0 flex items-center justify-center bg-neutral-900/60 pointer-events-none">
          <div class="flex gap-2">
            <For each={[0, 1, 2]}>
              {(i) => (
                <div class="w-3 h-3 rounded-full bg-neutral-600 animate-pulse" style={{ "animation-delay": `${i * 150}ms` }} />
              )}
            </For>
          </div>
        </div>
      </Show>

      <Show when={resolution.error && !isAbortError(resolution.error)}>
        <div class="absolute inset-0 flex flex-col items-center justify-center gap-3 text-neutral-400">
          <span class="text-sm">Failed to load flow: {String((resolution.error as Error)?.message ?? resolution.error)}</span>
          <button class="px-3 py-1 rounded bg-neutral-700 hover:bg-neutral-600 text-white text-xs" onClick={refetch}>
            Retry
          </button>
        </div>
      </Show>

      <Show when={!resolution.loading && !resolution.error && resolution() && !resolution()!.reachable}>
        <div class="absolute inset-0 flex items-center justify-center text-neutral-500 text-sm">
          No static path — try Path finder (UF.2) or check /api/unresolved for a ledgered gap.
        </div>
      </Show>

      <Show when={resolution()?.note}>
        <div
          data-testid="flow-lane-note"
          class="absolute top-2 right-2 z-10 max-w-xs px-2 py-1 rounded bg-neutral-800/90 border border-neutral-700 text-xs text-neutral-400"
        >
          {resolution()!.note}
        </div>
      </Show>

      {/* Truncated chains never silently end — an end-cap that re-queries
          deeper, same "narrow, don't guess" contract as the over-budget
          dialog (US.3). */}
      <Show when={resolution()?.truncated}>
        <button
          data-testid="flow-truncation-cap"
          class="absolute bottom-2 right-2 z-10 px-3 py-1.5 rounded bg-neutral-700 hover:bg-neutral-600 text-white text-xs"
          onClick={() => setThroughLimit((n) => n + 20)}
        >
          + more (depth limit)
        </button>
      </Show>
    </div>
  );
}
