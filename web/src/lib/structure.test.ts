import { describe, it, expect } from "vitest";
import { buildStructureView, structLabel, shortType } from "./structure";
import { GraphNode, GraphEdge } from "./types";

function node(id: string, over: Partial<GraphNode> = {}): GraphNode {
  return { id, type: "function", label: id, service: "svc", file: "a.go", line: 1, language: "go", ...over };
}

const user = node("user", {
  type: "struct",
  label: "User",
  meta: { fields: '[{"name":"Name","type":"string"},{"name":"Age","type":"int"}]' },
});
const counter = node("counter", { type: "variable", label: "counter", meta: { data_type: "int" } });
const bump = node("bump");
const orphan = node("orphan");
const handler = node("h", { type: "http_handler" });
const edges: GraphEdge[] = [
  { id: "e1", from: "bump", to: "counter", type: "writes" },
  { id: "e2", from: "bump", to: "user", type: "uses_type" },
  { id: "e3", from: "h", to: "bump", type: "calls" },
];

describe("structLabel", () => {
  it("renders struct fields as lines", () => {
    const label = structLabel(user);
    expect(label).toContain("User");
    expect(label).toContain("Name: string");
    expect(label).toContain("Age: int");
  });

  it("caps field lines", () => {
    const fields = Array.from({ length: 10 }, (_, i) => ({ name: `f${i}`, type: "int" }));
    const big = node("big", { type: "struct", label: "Big", meta: { fields: JSON.stringify(fields) } });
    const label = structLabel(big);
    expect(label).toContain("… 4 more");
  });

  it("annotates variables with their type", () => {
    expect(structLabel(counter)).toBe("counter: int");
  });

  it("strips package paths from types", () => {
    expect(shortType("github.com/x/y.User")).toBe("y.User");
  });
});

describe("buildStructureView", () => {
  it("keeps structural nodes and edges, drops the rest", () => {
    const r = buildStructureView([user, counter, bump, orphan, handler], edges);
    const ids = r.nodes.map((n) => n.id);
    expect(ids).toContain("user");
    expect(ids).toContain("counter");
    expect(ids).toContain("bump");
    expect(ids).not.toContain("h"); // http_handler is not a structure node
    expect(ids).not.toContain("orphan"); // disconnected function dropped
    expect(r.edges.map((e) => e.id).sort()).toEqual(["e1", "e2"]);
  });
});
