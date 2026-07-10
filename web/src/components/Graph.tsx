import { Component, createEffect, onMount, onCleanup, Show } from "solid-js";
import cytoscape from "cytoscape";
import dagre from "cytoscape-dagre";
import fcose from "cytoscape-fcose";
// @ts-ignore — no type definitions published
import svg from "cytoscape-svg";
import { graphStore } from "../stores/graph";
import { uiStore } from "../stores/ui";
import { visibleGraph } from "../stores/derived";
import { edgeConfidence } from "../lib/confidence";
import { BOUNDARY_GROUP_TYPE, isBoundaryGroupId } from "../lib/boundary";
import { FILE_GROUP_TYPE, isFileGroupId } from "../lib/filegroup";
import { SERVICE_NODE_TYPE } from "../lib/aggregate";
import { DEFAULT_NODE_COLOR, LABEL_COLOR, LANG_COLORS, NODE_TYPE_STYLES } from "../lib/styles";

cytoscape.use(dagre as any);
cytoscape.use(fcose as any);
cytoscape.use(svg as any);

// The current Cytoscape instance, exposed so the export menu can render
// SVG/PNG of exactly what is on screen.
let cyInstance: cytoscape.Core | undefined;
export function getCy(): cytoscape.Core | undefined {
  return cyInstance;
}

function inferLanguage(file: string): string {
  const ext = file.split(".").pop()?.toLowerCase() ?? "";
  if (ext === "go") return "go";
  if (ext === "rb") return "ruby";
  if (ext === "templ") return "templ";
  if (ext === "ts" || ext === "tsx") return "typescript";
  if (ext === "js" || ext === "jsx" || ext === "mjs") return "javascript";
  return "";
}

const STYLE: cytoscape.Stylesheet[] = [
  {
    selector: "node",
    style: {
      label: "data(label)",
      "background-color": DEFAULT_NODE_COLOR,
      "font-size": "10px",
      // Labels sit below the node on the dark canvas, so they must stay
      // light no matter what the node fill is.
      color: LABEL_COLOR,
      "text-valign": "bottom",
      "text-margin-y": 4,
      "text-max-width": "160px",
      "text-wrap": "ellipsis",
    } as any,
  },
  // Per-language colors (shared with the Legend via lib/styles)
  ...LANG_COLORS.map(([lang, color]) => ({
    selector: `node[language="${lang}"]`,
    style: { "background-color": color },
  })),
  // Node-type shapes/colors (shared with the Legend via lib/styles)
  ...NODE_TYPE_STYLES.map(({ type, shape, color }) => ({
    selector: `node[type="${type}"]`,
    style: { shape, ...(color ? { "background-color": color } : {}) } as any,
  })),
  // Collapsed framework/SDK boundary groups: compact, outlined, dimmed
  {
    selector: `node[type="${BOUNDARY_GROUP_TYPE}"]`,
    style: {
      "border-width": 1.5,
      "border-color": "#818cf8",
      "border-style": "dashed",
      color: "#c7d2fe",
      "font-size": "9px",
    } as any,
  },
  // Structure view: struct/class nodes show multi-line field labels
  {
    selector: 'node[type="struct"], node[type="class"]',
    style: {
      "text-wrap": "wrap",
      "text-max-width": "220px",
      "font-size": "9px",
    } as any,
  },
  // File group containers (compound parents) and their collapsed form
  {
    selector: `node[type="${FILE_GROUP_TYPE}"]`,
    style: {
      shape: "round-rectangle",
      "background-color": "#111827",
      "background-opacity": 0.45,
      "border-width": 1,
      "border-color": "#374151",
      color: "#9ca3af",
      "font-size": "9px",
      "text-valign": "top",
      "text-margin-y": -4,
    } as any,
  },
  {
    selector: `node[type="${FILE_GROUP_TYPE}"][collapsed="true"]`,
    style: {
      "border-style": "dashed",
      "border-color": "#6b7280",
      "background-opacity": 0.8,
      "text-valign": "center",
      "text-margin-y": 0,
    } as any,
  },
  // High-level service nodes
  {
    selector: `node[type="${SERVICE_NODE_TYPE}"]`,
    style: {
      width: 60,
      height: 40,
      "font-size": "12px",
      "text-valign": "center",
      "text-margin-y": 0,
    } as any,
  },
  {
    selector: "edge",
    style: {
      width: 1.5,
      "line-color": "#4b5563",
      "target-arrow-color": "#4b5563",
      "target-arrow-shape": "triangle",
      "curve-style": "bezier",
      label: "data(label)",
      "font-size": "8px",
      color: "#9ca3af",
      "text-rotation": "autorotate" as any,
    } as any,
  },
  // Variable-tracking edges: writes red, reads slate, captures orange
  // dashed, flows_to cyan.
  {
    selector: 'edge[type="writes"]',
    style: { "line-color": "#ef4444", "target-arrow-color": "#ef4444" } as any,
  },
  {
    selector: 'edge[type="reads"]',
    style: { "line-color": "#64748b", "target-arrow-color": "#64748b", opacity: 0.7 } as any,
  },
  {
    selector: 'edge[type="captures"]',
    style: { "line-color": "#f97316", "target-arrow-color": "#f97316", "line-style": "dashed" } as any,
  },
  {
    selector: 'edge[type="flows_to"]',
    style: { "line-color": "#22d3ee", "target-arrow-color": "#22d3ee" } as any,
  },
  // Uncertain edges: dashed + dimmed, visually distinct from confirmed flow
  {
    selector: 'edge[confidence="partial"], edge[confidence="unknown"]',
    style: { "line-style": "dashed", "line-color": "#6b7280", opacity: 0.6 } as any,
  },
  { selector: "node:selected", style: { "border-width": 2, "border-color": "#fff" } },
  // Trace root gets a bright ring
  { selector: "node.trace-root", style: { "border-width": 3, "border-color": "#f472b6" } as any },
  // Neighborhood highlight on selection
  { selector: "node.dimmed", style: { opacity: 0.25 } as any },
  { selector: "edge.dimmed", style: { opacity: 0.15 } as any },
];

const Graph: Component = () => {
  let containerRef: HTMLDivElement | undefined;
  let cy: cytoscape.Core | undefined;

  onMount(() => {
    if (!containerRef) return;

    cy = cytoscape({
      container: containerRef,
      elements: [],
      style: STYLE,
      layout: { name: "dagre" },
      wheelSensitivity: 0.3,
    });
    cyInstance = cy;

    cy.on("tap", "node", (evt) => {
      uiStore.setSelectedNodeId(evt.target.data("id") as string);
    });
    cy.on("tap", (evt) => {
      if (evt.target === cy) uiStore.setSelectedNodeId(null);
    });
    // Double-tap a collapsed boundary group to expand it in place, or a
    // file group to collapse/expand the file.
    cy.on("dbltap", "node", (evt) => {
      const id = evt.target.data("id") as string;
      if (isBoundaryGroupId(id)) uiStore.toggleBoundary(id);
      else if (isFileGroupId(id)) uiStore.toggleFileCollapse(id);
    });

    // Load the full graph on mount unless a trace is being restored from URL.
    if (!graphStore.traceRoot()) {
      graphStore.fetchGraph();
    }
    graphStore.fetchStats();
  });

  // Re-render Cytoscape whenever the visible graph or layout changes.
  createEffect(() => {
    const { nodes, edges } = visibleGraph();
    const layout = uiStore.layout();
    const root = graphStore.traceRoot();
    if (!cy) return;

    cy.elements().remove();

    cy.add(
      nodes.map((n) => ({
        group: "nodes" as const,
        data: {
          id: n.id,
          label: n.label,
          type: n.type,
          service: n.service,
          file: n.file,
          line: n.line,
          language: n.language || inferLanguage(n.file),
          ...(n.parent ? { parent: n.parent } : {}),
          ...(n.meta?.collapsed ? { collapsed: n.meta.collapsed } : {}),
        },
      }))
    );

    cy.add(
      edges.map((e) => ({
        group: "edges" as const,
        data: {
          id: e.id,
          source: e.from,
          target: e.to,
          type: e.type,
          label: e.label ?? "",
          confidence: edgeConfidence(e),
        },
      }))
    );

    if (root) {
      cy.getElementById(root).addClass("trace-root");
    }

    cy.resize();
    // dagre has no compound-node support — parents end up overlapping. When
    // file containers are on screen, silently run fcose (compound-aware)
    // instead; the toolbar still shows the user's chosen layout.
    const hasCompound = nodes.some((n) => n.parent);
    const effective = hasCompound && layout === "dagre" ? "fcose" : layout;
    cy.layout({ name: effective, fit: true, padding: 30 } as any).run();
  });

  // Dim everything outside the selected node's neighborhood.
  createEffect(() => {
    const id = uiStore.selectedNodeId();
    if (!cy) return;
    cy.elements().removeClass("dimmed");
    if (!id) return;
    const sel = cy.getElementById(id);
    if (sel.empty()) return;
    const hood = sel.closedNeighborhood();
    cy.elements().not(hood).addClass("dimmed");
  });

  onCleanup(() => {
    cy?.destroy();
    cyInstance = undefined;
  });

  return (
    <div class="relative w-full h-full">
      <div ref={containerRef} class="w-full h-full" />
      <Show when={graphStore.loading()}>
        <div class="absolute inset-0 flex items-center justify-center bg-gray-950/40 pointer-events-none">
          <span class="text-sm text-gray-300 animate-pulse">Loading graph…</span>
        </div>
      </Show>
      <Show when={graphStore.error()}>
        <div class="absolute top-3 left-1/2 -translate-x-1/2 bg-red-900/90 border border-red-600 text-red-100 text-xs rounded px-3 py-2">
          {graphStore.error()}
        </div>
      </Show>
      <Show when={!graphStore.loading() && visibleGraph().nodes.length === 0}>
        <div class="absolute inset-0 flex items-center justify-center pointer-events-none">
          <span class="text-sm text-gray-500">
            No nodes to show — run <code class="text-gray-400">polyflow index</code> or relax the filters.
          </span>
        </div>
      </Show>
    </div>
  );
};

export default Graph;
