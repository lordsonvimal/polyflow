import { Component, createEffect, onMount, onCleanup } from "solid-js";
import cytoscape from "cytoscape";
import dagre from "cytoscape-dagre";
import fcose from "cytoscape-fcose";
import { graphStore } from "../stores/graph";
import { uiStore } from "../stores/ui";

cytoscape.use(dagre as any);
cytoscape.use(fcose as any);

const Graph: Component = () => {
  let containerRef: HTMLDivElement | undefined;
  let cy: cytoscape.Core | undefined;

  onMount(() => {
    if (!containerRef) return;

    cy = cytoscape({
      container: containerRef,
      elements: [],
      style: [
        {
          selector: "node",
          style: {
            label: "data(label)",
            "background-color": "#6366f1",
            "font-size": "10px",
            color: "#f9fafb",
            "text-valign": "bottom",
            "text-margin-y": 4,
          },
        },
        {
          selector: "edge",
          style: {
            width: 1.5,
            "line-color": "#4b5563",
            "target-arrow-color": "#4b5563",
            "target-arrow-shape": "triangle",
            "curve-style": "bezier",
            "font-size": "8px",
            label: "data(label)",
          },
        },
        {
          selector: "node:selected",
          style: { "background-color": "#a5b4fc", "border-width": 2, "border-color": "#fff" },
        },
      ],
      layout: { name: "dagre" },
    });

    cy.on("tap", "node", (evt) => {
      const id = evt.target.data("id") as string;
      uiStore.setSelectedNodeId(id);
    });

    // Load the full graph on mount
    graphStore.fetchGraph();
  });

  // Re-render Cytoscape whenever the store changes
  createEffect(() => {
    const nodes = graphStore.nodes();
    const edges = graphStore.edges();
    const layout = uiStore.layout();
    if (!cy) return;

    cy.elements().remove();

    cy.add(
      nodes.map((n) => ({
        group: "nodes" as const,
        data: { id: n.id, label: n.label, type: n.type, service: n.service, file: n.file, line: n.line },
      }))
    );

    cy.add(
      edges.map((e) => ({
        group: "edges" as const,
        data: { id: e.id, source: e.from, target: e.to, type: e.type, label: e.label ?? "" },
      }))
    );

    cy.resize();
    cy.layout({ name: layout } as any).run();
  });

  onCleanup(() => {
    cy?.destroy();
  });

  return <div ref={containerRef} class="w-full h-full" />;
};

export default Graph;
