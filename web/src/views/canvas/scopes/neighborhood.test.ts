import { describe, it, expect, vi } from "vitest";
import { resolveNeighborhood } from "./neighborhood";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

const TRACE = {
  nodes: [
    { data: { id: "n2", label: "b", type: "function", service: "auth", file: "auth/b.go", line: 1, language: "go" } },
    { data: { id: "n1", label: "a", type: "function", service: "auth", file: "auth/a.go", line: 1, language: "go" } },
  ],
  edges: [{ data: { id: "e1", source: "n1", target: "n2", type: "calls" } }],
};

describe("resolveNeighborhood", () => {
  it("fetches the trace subgraph and sorts elements by id", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/graph/trace?root=n1&direction=both&depth=2": TRACE });
    const d = await resolveNeighborhood({ kind: "neighborhood", nodeId: "n1", depth: 2 });
    expect(d.nodes.map((x) => x.id)).toEqual(["n1", "n2"]); // sorted, not insertion order
  });

  it("is deterministic across two runs", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/graph/trace?root=n1&direction=both&depth=2": TRACE });
    const a = await resolveNeighborhood({ kind: "neighborhood", nodeId: "n1", depth: 2 });
    const b = await resolveNeighborhood({ kind: "neighborhood", nodeId: "n1", depth: 2 });
    expect(a).toEqual(b);
  });
});
