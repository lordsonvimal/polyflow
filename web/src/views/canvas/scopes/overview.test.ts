import { describe, it, expect, vi } from "vitest";
import { resolveOverview } from "./overview";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

function n(id: string, service: string) {
  return { data: { id, label: id, type: "function", service, file: `${service}/a.go`, line: 1, language: "go" } };
}
function e(id: string, source: string, target: string, type = "calls") {
  return { data: { id, source, target, type } };
}

const GRAPH = {
  nodes: [n("a1", "svcA"), n("a2", "svcA"), n("b1", "svcB")],
  edges: [
    e("e1", "a1", "a2"), // same-service — must not appear in overview
    e("e2", "a1", "b1", "http_call"),
    e("e3", "a2", "b1", "http_call"),
  ],
};

describe("resolveOverview", () => {
  it("aggregates one node per service and cross-service edges only", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/graph?limit=2000": GRAPH });
    const d = await resolveOverview();
    expect(d.nodes.map((x) => x.id)).toEqual(["service:svcA", "service:svcB"]);
    expect(d.edges).toHaveLength(1);
    expect(d.edges[0]).toMatchObject({ from: "service:svcA", to: "service:svcB", type: "http_call", label: "http_call ×2" });
  });

  it("never includes a same-service edge (negative)", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/graph?limit=2000": GRAPH });
    const d = await resolveOverview();
    expect(d.edges.find((x) => x.type === "calls")).toBeUndefined();
  });

  it("is deterministic across two runs", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/graph?limit=2000": GRAPH });
    const a = await resolveOverview();
    const b = await resolveOverview();
    expect(a).toEqual(b);
  });
});
