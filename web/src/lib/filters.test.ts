import { describe, it, expect } from "vitest";
import { applyFilters, type FilterableGraph } from "./filters";
import { GraphNode, GraphEdge } from "./types";

function node(id: string, service: string): GraphNode {
  return { id, type: "function", label: id, service, file: `${id}.go`, line: 1, language: "go" };
}
function edge(id: string, from: string, to: string, type: string, confidence?: string): GraphEdge {
  return { id, from, to, type, confidence };
}

const GRAPH: FilterableGraph = {
  nodes: [node("a", "svc1"), node("b", "svc1"), node("c", "svc2")],
  edges: [
    edge("e1", "a", "b", "calls"), // no confidence -> static (lib/confidence.ts)
    edge("e2", "b", "c", "http_call", "inferred"),
    edge("e3", "a", "c", "publishes", "partial"),
    edge("e4", "a", "b", "reads", "unknown"),
  ],
};

describe("applyFilters", () => {
  it("defaults to static+inferred confidence, all edge-type groups, all services", () => {
    const out = applyFilters(GRAPH, { confidence: [], edgeTypes: [], services: [] });
    expect(out.nodes.map((n) => n.id)).toEqual(["a", "b", "c"]);
    expect(out.edges.map((e) => e.id)).toEqual(["e1", "e2"]); // e3/e4 are partial/unknown, opt-in only
  });

  it("opting a confidence tier in surfaces its edges", () => {
    const out = applyFilters(GRAPH, { confidence: ["static", "inferred", "partial"], edgeTypes: [], services: [] });
    expect(out.edges.map((e) => e.id)).toEqual(["e1", "e2", "e3"]);
  });

  it("restricting to one edge-type group drops the rest", () => {
    const out = applyFilters(GRAPH, { confidence: ["static", "inferred"], edgeTypes: ["calls"], services: [] });
    expect(out.edges.map((e) => e.id)).toEqual(["e1"]);
  });

  it("restricting services drops nodes and any edge touching a dropped node", () => {
    const out = applyFilters(GRAPH, { confidence: ["static", "inferred", "partial", "unknown"], edgeTypes: [], services: ["svc1"] });
    expect(out.nodes.map((n) => n.id)).toEqual(["a", "b"]);
    // e2 (b->c) and e3 (a->c) both lose their svc2 endpoint
    expect(out.edges.map((e) => e.id)).toEqual(["e1", "e4"]);
  });

  it("is a pure function — never mutates its input", () => {
    const before = JSON.stringify(GRAPH);
    applyFilters(GRAPH, { confidence: [], edgeTypes: ["dom"], services: ["svc1"] });
    expect(JSON.stringify(GRAPH)).toBe(before);
  });
});
