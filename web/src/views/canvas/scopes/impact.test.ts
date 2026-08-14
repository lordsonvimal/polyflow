import { describe, it, expect, vi } from "vitest";
import { resolveImpact, computeImpactDepths, roleForDepth } from "./impact";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

describe("computeImpactDepths", () => {
  const edges = [
    { from: "root", to: "a" },
    { from: "a", to: "b" },
    { from: "root", to: "c" },
  ];

  it("walks forward for 'down'", () => {
    const d = computeImpactDepths(edges, "root", "down");
    expect(d.get("root")).toBe(0);
    expect(d.get("a")).toBe(1);
    expect(d.get("b")).toBe(2);
    expect(d.get("c")).toBe(1);
  });

  it("walks reversed for 'up'", () => {
    const d = computeImpactDepths(edges, "b", "up");
    expect(d.get("b")).toBe(0);
    expect(d.get("a")).toBe(1);
    expect(d.get("root")).toBe(2);
    expect(d.has("c")).toBe(false);
  });

  it("treats edges as undirected for 'both'", () => {
    const d = computeImpactDepths(edges, "b", "both");
    expect(d.get("b")).toBe(0);
    expect(d.get("a")).toBe(1);
    expect(d.get("root")).toBe(2);
    expect(d.get("c")).toBe(3);
  });
});

describe("roleForDepth", () => {
  it("classifies target/direct/transitive by BFS distance", () => {
    expect(roleForDepth(0)).toBe("target");
    expect(roleForDepth(1)).toBe("direct");
    expect(roleForDepth(2)).toBe("transitive");
    expect(roleForDepth(5)).toBe("transitive");
  });
});

describe("resolveImpact", () => {
  const TRACE = {
    nodes: [
      { data: { id: "b", label: "b", type: "function", service: "auth", file: "auth/b.go", line: 1, language: "go" } },
      { data: { id: "root", label: "root", type: "function", service: "auth", file: "auth/root.go", line: 1, language: "go" } },
      { data: { id: "a", label: "a", type: "function", service: "auth", file: "auth/a.go", line: 1, language: "go" } },
    ],
    edges: [
      { data: { id: "e1", source: "root", target: "a", type: "calls" } },
      { data: { id: "e2", source: "a", target: "b", type: "calls" } },
    ],
  };

  it("maps the UI direction to the server's forward/backward/both", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/graph/trace?root=root&direction=forward&depth=3": TRACE });
    const d = await resolveImpact({ kind: "impact", target: "root", direction: "down", depth: 3 });
    expect(d.nodes.map((n) => n.id)).toEqual(["a", "b", "root"]); // sorted
  });

  it("tags each node with impact_role/impact_depth from BFS over the returned edges", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/graph/trace?root=root&direction=forward&depth=3": TRACE });
    const d = await resolveImpact({ kind: "impact", target: "root", direction: "down", depth: 3 });
    const byId = Object.fromEntries(d.nodes.map((n) => [n.id, n.meta]));
    expect(byId.root).toMatchObject({ impact_role: "target", impact_depth: "0" });
    expect(byId.a).toMatchObject({ impact_role: "direct", impact_depth: "1" });
    expect(byId.b).toMatchObject({ impact_role: "transitive", impact_depth: "2" });
  });
});
