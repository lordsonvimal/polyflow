import { describe, it, expect } from "vitest";
import {
  applyFileGrouping,
  fileGroupId,
  isFileGroupId,
  fileBasename,
  FILE_GROUP_TYPE,
} from "./filegroup";
import { GraphNode, GraphEdge } from "./types";

function node(id: string, over: Partial<GraphNode> = {}): GraphNode {
  return { id, type: "function", label: id, service: "svc", file: "src/a.go", line: 1, language: "go", ...over };
}

const a1 = node("a1");
const a2 = node("a2", { line: 20 });
const b1 = node("b1", { file: "src/b.go" });
const noFile = node("dstore", { type: "datastore", file: "" });
const edges: GraphEdge[] = [
  { id: "e1", from: "a1", to: "a2", type: "calls" }, // inside a.go
  { id: "e2", from: "a2", to: "b1", type: "calls" }, // a.go -> b.go
  { id: "e3", from: "b1", to: "dstore", type: "queries" },
];

describe("helpers", () => {
  it("builds and recognises group ids", () => {
    const id = fileGroupId("svc", "src/a.go");
    expect(isFileGroupId(id)).toBe(true);
    expect(isFileGroupId("a1")).toBe(false);
  });
  it("extracts basenames", () => {
    expect(fileBasename("src/a.go")).toBe("a.go");
    expect(fileBasename("a.go")).toBe("a.go");
  });
});

describe("applyFileGrouping (expanded by default)", () => {
  it("wraps file-bearing nodes in compound parents", () => {
    const r = applyFileGrouping([a1, a2, b1, noFile], edges, []);
    const aGroup = fileGroupId("svc", "src/a.go");
    const bGroup = fileGroupId("svc", "src/b.go");

    const byId = new Map(r.nodes.map((n) => [n.id, n]));
    expect(byId.get("a1")?.parent).toBe(aGroup);
    expect(byId.get("a2")?.parent).toBe(aGroup);
    expect(byId.get("b1")?.parent).toBe(bGroup);
    expect(byId.get("dstore")?.parent).toBeUndefined();

    const groupNode = byId.get(aGroup)!;
    expect(groupNode.type).toBe(FILE_GROUP_TYPE);
    expect(groupNode.label).toBe("a.go");
    expect(groupNode.meta?.member_count).toBe("2");

    // Edges pass through untouched when nothing is collapsed.
    expect(r.edges).toHaveLength(3);
    expect(r.groups).toHaveLength(2);
  });

  it("does not re-wrap existing group nodes", () => {
    const first = applyFileGrouping([a1], [], []);
    const second = applyFileGrouping(first.nodes, [], []);
    const groupNodes = second.nodes.filter((n) => n.type === FILE_GROUP_TYPE);
    expect(groupNodes).toHaveLength(1);
  });
});

describe("applyFileGrouping (collapsed files)", () => {
  const aGroup = fileGroupId("svc", "src/a.go");

  it("folds a collapsed file into a single node", () => {
    const r = applyFileGrouping([a1, a2, b1, noFile], edges, [aGroup]);
    const ids = r.nodes.map((n) => n.id);
    expect(ids).not.toContain("a1");
    expect(ids).not.toContain("a2");
    expect(ids).toContain(aGroup);

    const groupNode = r.nodes.find((n) => n.id === aGroup)!;
    expect(groupNode.label).toBe("a.go (2)");
    expect(groupNode.meta?.collapsed).toBe("true");
    expect(groupNode.parent).toBeUndefined();
  });

  it("re-routes and drops edges across the collapsed file", () => {
    const r = applyFileGrouping([a1, a2, b1, noFile], edges, [aGroup]);
    // e1 was inside a.go → dropped; e2 re-routed from group → b1.
    const rerouted = r.edges.find((e) => e.from === aGroup && e.to === "b1");
    expect(rerouted).toBeDefined();
    expect(rerouted!.type).toBe("calls");
    expect(r.edges).toHaveLength(2);
  });

  it("deduplicates re-routed edges", () => {
    const dup: GraphEdge[] = [
      { id: "x1", from: "a1", to: "b1", type: "calls" },
      { id: "x2", from: "a2", to: "b1", type: "calls" },
    ];
    const r = applyFileGrouping([a1, a2, b1], dup, [aGroup]);
    expect(r.edges).toHaveLength(1);
  });
});
