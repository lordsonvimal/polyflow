import { describe, it, expect, vi } from "vitest";
import { resolveFolder } from "./folder";

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
    e("e1", "n1", "n2", "calls"), // within folder "app", crosses its child groups — visible at folder granularity
    e("e2", "n1", "n3", "http_call"), // leaves "app" for a sibling top-level file — boundary stub
    e("e3", "n1", "n4", "publishes"), // leaves the service entirely — boundary stub
  ],
};

function routes() {
  return { "/api/tree?service=svcA": TREE, "/api/graph?limit=2000": GRAPH };
}

describe("resolveFolder", () => {
  it("collapses immediate children of the folder with cross-group edges", async () => {
    (globalThis as any).fetch = fakeFetch(routes());
    const d = await resolveFolder({ kind: "folder", service: "svcA", path: "app" });
    expect(d.nodes.map((x) => x.id)).toEqual(["fileA", "fileTop", "folder:svcA:app/sub", "service:svcB"]);
    const cross = d.edges.find((x) => x.type === "calls");
    expect(cross).toMatchObject({ from: "fileA", to: "folder:svcA:app/sub" });
  });

  it("renders a sibling top-level file as a boundary stub, never expanded (negative)", async () => {
    (globalThis as any).fetch = fakeFetch(routes());
    const d = await resolveFolder({ kind: "folder", service: "svcA", path: "app" });
    const stub = d.nodes.find((x) => x.id === "fileTop");
    expect(stub).toBeTruthy();
    expect(stub!.meta?.stub).toBe("true");
    expect(d.nodes.some((x) => x.id === "n3")).toBe(false);
  });

  it("renders a different service as a boundary stub", async () => {
    (globalThis as any).fetch = fakeFetch(routes());
    const d = await resolveFolder({ kind: "folder", service: "svcA", path: "app" });
    const stub = d.nodes.find((x) => x.id === "service:svcB");
    expect(stub?.meta?.stub).toBe("true");
  });

  it("is deterministic across two runs", async () => {
    (globalThis as any).fetch = fakeFetch(routes());
    const a = await resolveFolder({ kind: "folder", service: "svcA", path: "app" });
    const b = await resolveFolder({ kind: "folder", service: "svcA", path: "app" });
    expect(a).toEqual(b);
  });
});
