import { describe, it, expect } from "vitest";
import {
  ALL_EDGE_TYPES,
  LENSES,
  LENS_NAMES,
  LENS_REMAINDER,
  edgeTypesForLens,
  applyLens,
  aggregateImportsRollup,
} from "./lenses";
import { GraphNode, GraphEdge } from "../../lib/types";

function node(id: string, file: string, service = "svc1"): GraphNode {
  return { id, type: "function", label: id, service, file, line: 1, language: "go" };
}
function edge(id: string, from: string, to: string, type: string): GraphEdge {
  return { id, from, to, type };
}

describe("rule-12 enum-coverage walk", () => {
  it("maps every graph.EdgeType to exactly one lens or the pinned remainder", () => {
    const seen = new Map<string, string>();
    for (const [lens, types] of Object.entries(LENSES)) {
      for (const t of types) {
        expect(seen.has(t), `"${t}" appears in both "${seen.get(t)}" and "${lens}"`).toBe(false);
        seen.set(t, lens);
      }
    }
    for (const t of LENS_REMAINDER) {
      expect(seen.has(t), `"${t}" is in both the remainder and lens "${seen.get(t)}"`).toBe(false);
      seen.set(t, "remainder");
    }
    for (const t of ALL_EDGE_TYPES) {
      expect(seen.has(t), `"${t}" is not mapped to any lens or the remainder`).toBe(true);
    }
    // No stray types in the table that aren't part of the mirrored master list.
    for (const t of seen.keys()) {
      expect(ALL_EDGE_TYPES.includes(t), `"${t}" is mapped but missing from ALL_EDGE_TYPES`).toBe(true);
    }
  });

  it("LENS_NAMES has All plus every LENSES key, no more no less", () => {
    expect(LENS_NAMES.slice(1).sort()).toEqual(Object.keys(LENSES).sort());
  });
});

describe("edgeTypesForLens", () => {
  it("returns null for All (sentinel: everything except remainder)", () => {
    expect(edgeTypesForLens("All")).toBeNull();
  });

  it("returns the exact pinned list for a named lens", () => {
    expect(edgeTypesForLens("Calls")).toEqual(["calls", "spawns", "instantiates"]);
  });

  it("falls back to null (All) for an unrecognized lens name", () => {
    expect(edgeTypesForLens("bogus")).toBeNull();
  });
});

describe("applyLens", () => {
  const graph = {
    nodes: [node("a", "a.go"), node("b", "b.go"), node("c", "c.go")],
    edges: [
      edge("e1", "a", "b", "calls"),
      edge("e2", "b", "c", "reads"),
      edge("e3", "a", "c", "contains"),
    ],
  };

  it("All hides contains but keeps everything else", () => {
    const out = applyLens(graph, "All", { hideUnlinked: false });
    expect(out.edges.map((e) => e.id)).toEqual(["e1", "e2"]);
  });

  it("a named lens keeps only its edge types", () => {
    const out = applyLens(graph, "Calls", { hideUnlinked: false });
    expect(out.edges.map((e) => e.id)).toEqual(["e1"]);
  });

  it("dims (does not remove) nodes with no visible edge by default", () => {
    const out = applyLens(graph, "Calls", { hideUnlinked: false });
    expect(out.nodes.map((n) => n.id)).toEqual(["a", "b", "c"]);
    expect(out.nodes.find((n) => n.id === "c")?.meta?.lens_dim).toBe("true");
    expect(out.nodes.find((n) => n.id === "a")?.meta?.lens_dim).toBeUndefined();
  });

  it("hideUnlinked removes unlinked nodes and any edge touching them", () => {
    const out = applyLens(graph, "Calls", { hideUnlinked: true });
    expect(out.nodes.map((n) => n.id)).toEqual(["a", "b"]);
    expect(out.edges.map((e) => e.id)).toEqual(["e1"]);
  });

  it("is a pure function — never mutates its input", () => {
    const before = JSON.stringify(graph);
    applyLens(graph, "Calls", { hideUnlinked: false });
    expect(JSON.stringify(graph)).toBe(before);
  });
});

describe("aggregateImportsRollup", () => {
  const graph = {
    nodes: [
      node("f1", "a.go", "svc1"),
      node("f2", "a.go", "svc1"),
      node("g1", "b.go", "svc1"),
      node("h1", "c.go", "svc2"),
    ],
    edges: [
      edge("i1", "f1", "g1", "imports"),
      edge("i2", "f2", "g1", "imports"),
      edge("i3", "g1", "h1", "imports"),
      edge("i4", "f1", "f2", "imports"), // same-file, dropped
    ],
  };

  it("collapses concrete edges into one edge per (fromFile, toFile) with a count", () => {
    const out = aggregateImportsRollup(graph);
    expect(out.edges.map((e) => e.id)).toEqual(["rollup:a.go->b.go", "rollup:b.go->c.go"]);
    expect(out.edges[0].meta?.rollup_count).toBe("2");
    expect(out.edges[0].label).toBe("imports ×2");
    expect(out.edges[1].meta?.rollup_count).toBe("1");
  });

  it("emits one node per distinct file, carrying the file's service", () => {
    const out = aggregateImportsRollup(graph);
    const ids = out.nodes.map((n) => n.id).sort();
    expect(ids).toEqual(["rollup-node:a.go", "rollup-node:b.go", "rollup-node:c.go"]);
    expect(out.nodes.find((n) => n.file === "c.go")?.service).toBe("svc2");
  });

  it("the detail map drills a rollup edge back to its concrete edges", () => {
    const out = aggregateImportsRollup(graph);
    const concrete = out.detail.get("rollup:a.go->b.go");
    expect(concrete?.map((e) => e.id).sort()).toEqual(["i1", "i2"]);
  });

  it("two runs on the same input produce byte-identical output (rule 2)", () => {
    const a = aggregateImportsRollup(graph);
    const b = aggregateImportsRollup(graph);
    expect(JSON.stringify({ nodes: a.nodes, edges: a.edges })).toBe(
      JSON.stringify({ nodes: b.nodes, edges: b.edges })
    );
  });
});
