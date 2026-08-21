import { describe, it, expect, vi } from "vitest";
import { resolveService } from "./service";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

const TREE = {
  service: "svcA",
  tree: [
    {
      kind: "folder",
      name: "app",
      path: "app",
      children: [
        { kind: "folder", name: "sub", path: "app/sub", children: [{ kind: "file", name: "b.go", path: "app/sub/b.go", node_id: "fileB", children: [] }] },
        { kind: "file", name: "a.go", path: "app/a.go", node_id: "fileA", children: [] },
      ],
    },
    { kind: "file", name: "top.go", path: "top.go", node_id: "fileTop", children: [] },
  ],
  counts: { folders: 2, files: 3, symbols: 0 },
};

function n(id: string, service: string, file: string) {
  return { data: { id, label: id, type: "function", service, file, line: 1, language: "go" } };
}
function e(id: string, source: string, target: string, type = "calls") {
  return { data: { id, source, target, type } };
}

const GRAPH = {
  nodes: [n("n1", "svcA", "app/a.go"), n("n2", "svcA", "app/sub/b.go"), n("n3", "svcA", "top.go"), n("n4", "svcB", "b/x.go")],
  edges: [
    e("e1", "n1", "n2"), // both under top-level folder "app" — intra-group at service scope
    e("e2", "n1", "n3", "http_call"), // cross top-level group (folder "app" -> file "top.go")
    e("e3", "n1", "n4", "publishes"), // boundary to another service
  ],
};

function routes() {
  return { "/api/tree?service=svcA": TREE, "/api/graph?limit=2000&page=1": GRAPH };
}

describe("resolveService", () => {
  it("collapses top-level folders/files into compounds with cross-group edges", async () => {
    (globalThis as any).fetch = fakeFetch(routes());
    const d = await resolveService({ kind: "service", service: "svcA" });
    expect(d.nodes.map((x) => x.id)).toEqual(["fileTop", "folder:svcA:app", "service:svcB"]);
    expect(d.nodes.find((x) => x.id === "folder:svcA:app")?.meta?.node_count).toBe("2");
    const cross = d.edges.find((x) => x.type === "http_call");
    expect(cross).toMatchObject({ from: "folder:svcA:app", to: "fileTop" });
  });

  it("never expands a boundary service into its own nodes (negative)", async () => {
    (globalThis as any).fetch = fakeFetch(routes());
    const d = await resolveService({ kind: "service", service: "svcA" });
    const stub = d.nodes.find((x) => x.id === "service:svcB");
    expect(stub).toBeTruthy();
    expect(stub!.meta?.stub).toBe("true");
    expect(d.nodes.some((x) => x.id === "n4")).toBe(false); // the real svcB node is never pulled in
    const boundaryEdge = d.edges.find((x) => x.type === "publishes");
    expect(boundaryEdge).toMatchObject({ from: "folder:svcA:app", to: "service:svcB" });
  });

  it("drops the intra-folder edge entirely at service granularity (negative)", async () => {
    (globalThis as any).fetch = fakeFetch(routes());
    const d = await resolveService({ kind: "service", service: "svcA" });
    expect(d.edges.find((x) => x.type === "calls")).toBeUndefined();
  });

  it("is deterministic across two runs", async () => {
    (globalThis as any).fetch = fakeFetch(routes());
    const a = await resolveService({ kind: "service", service: "svcA" });
    const b = await resolveService({ kind: "service", service: "svcA" });
    expect(a).toEqual(b);
  });

  // UN.8: a group pair with traffic both ways (e.g. an http_call out, an
  // sse_endpoint push back) must collapse to one edge, not two overlapping
  // ones — and keep the *first-observed* direction rather than force-
  // flipping to alphabetical node-id order.
  it("collapses a bidirectional group pair into one edge, keeping the first-observed direction", async () => {
    const biGraph = {
      nodes: [n("n1", "svcA", "app/a.go"), n("n2", "svcA", "top.go")],
      edges: [e("e1", "n1", "n2", "http_call"), e("e2", "n2", "n1", "sse_endpoint")],
    };
    (globalThis as any).fetch = fakeFetch({ "/api/tree?service=svcA": TREE, "/api/graph?limit=2000&page=1": biGraph });
    const d = await resolveService({ kind: "service", service: "svcA" });
    const cross = d.edges.filter((x) => x.from === "folder:svcA:app" || x.to === "folder:svcA:app");
    expect(cross).toHaveLength(1);
    expect(cross[0]).toMatchObject({ from: "folder:svcA:app", to: "fileTop", type: "cross_service" });
    expect(cross[0].meta?.bidirectional).toBe("true");
  });
});
