import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { treeStore, buildRows, rowKeyFor, serviceOfNodeId, type ApiTreeResult, type ServiceEntry } from "./tree";
import { connectionStore } from "./connection";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k || key.startsWith(k));
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

const AUTH_TREE: ApiTreeResult = {
  service: "auth",
  tree: [
    {
      kind: "folder",
      name: "app",
      path: "app",
      children: [
        {
          kind: "file",
          name: "user.rb",
          path: "app/user.rb",
          node_id: "auth:app/user.rb:file::0",
          children: [
            {
              kind: "class",
              name: "User",
              node_id: "auth:app/user.rb:class:User:1",
              line: 1,
              end_line: 40,
              children: [
                {
                  kind: "method",
                  name: "save",
                  node_id: "auth:app/user.rb:method:save:5",
                  line: 5,
                  end_line: 10,
                  children: [],
                },
              ],
            },
          ],
        },
      ],
    },
  ],
  counts: { folders: 1, files: 1, symbols: 2 },
};

describe("tree store", () => {
  beforeEach(() => {
    (globalThis as any).fetch = fakeFetch({
      "/api/stack": { services: [{ name: "auth", language: "ruby", frameworks: [], files: 1 }] },
      "/api/tree?service=auth": AUTH_TREE,
      "/api/unresolved?service=auth": {
        refs: [
          { service: "auth", file: "app/user.rb", line: 3, name: "Foo", kind: "call" },
          { service: "auth", file: "app/other.rb", line: 1, name: "Bar", kind: "call" },
        ],
        total: 2,
        page: 1,
      },
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    treeStore.stop();
  });

  it("derives the owning service from a node id (service:file:type:name:line)", () => {
    expect(serviceOfNodeId("auth:app/user.rb:class:User:1")).toBe("auth");
  });

  it("loadServices maps the /api/stack wire shape (deps/node_counts/edge_counts) into ServiceSummary (UN.4)", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/stack": {
        services: [
          {
            name: "auth",
            language: "ruby",
            frameworks: ["rails"],
            files: 3,
            deps: [{ name: "rails", version: "7.1.0", ecosystem: "rubygems" }],
            node_counts: { method: 5 },
            edge_counts: { calls: 9 },
          },
          { name: "empty-svc", language: "", frameworks: [], files: 0 }, // no deps/counts at all
        ],
      },
    });
    await treeStore.loadServices();
    expect(treeStore.services()).toEqual([
      {
        name: "auth",
        language: "ruby",
        frameworks: ["rails"],
        files: 3,
        deps: [{ name: "rails", version: "7.1.0", ecosystem: "rubygems" }],
        nodeCounts: { method: 5 },
        edgeCounts: { calls: 9 },
      },
      { name: "empty-svc", language: "", frameworks: [], files: 0, deps: [], nodeCounts: {}, edgeCounts: {} },
    ]);
  });

  describe("buildRows", () => {
    it("shows only the service row when collapsed", () => {
      const services = [{ name: "auth", language: "ruby", frameworks: [], files: 1 }];
      const rows = buildRows(services, {}, new Set());
      expect(rows).toEqual([{ key: "svc:auth", depth: 0, kind: "service", name: "auth", service: "auth", hasChildren: true }]);
    });

    it("flattens nested folder/file/class/method rows when expanded, and stops at a collapsed node", () => {
      const services = [{ name: "auth", language: "ruby", frameworks: [], files: 1 }];
      const entry: ServiceEntry = { tree: AUTH_TREE, loading: false };
      const expanded = new Set(["svc:auth", "svc:auth:folder:app", "svc:auth:file:app/user.rb"]);
      const rows = buildRows(services, { auth: entry }, expanded);
      const kinds = rows.map((r) => r.kind);
      // service, folder, file, class — method is NOT included because the
      // class row (its parent) was never expanded.
      expect(kinds).toEqual(["service", "folder", "file", "class"]);
      const classRow = rows.find((r) => r.kind === "class")!;
      expect(classRow.hasChildren).toBe(true);
      expect(classRow.file).toBe("app/user.rb");
    });

    it("shows a loading placeholder while a service's tree is in flight", () => {
      const services = [{ name: "auth", language: "ruby", frameworks: [], files: 1 }];
      const rows = buildRows(services, { auth: { loading: true } }, new Set(["svc:auth"]));
      expect(rows[1].kind).toBe("__loading__");
    });
  });

  it("rowKeyFor is stable across folder/file/symbol kinds", () => {
    expect(rowKeyFor("auth", { kind: "folder", name: "app", path: "app", children: [] })).toBe("svc:auth:folder:app");
    expect(rowKeyFor("auth", { kind: "file", name: "user.rb", path: "app/user.rb", children: [] })).toBe(
      "svc:auth:file:app/user.rb",
    );
    expect(
      rowKeyFor("auth", { kind: "class", name: "User", node_id: "auth:app/user.rb:class:User:1", children: [] }),
    ).toBe("svc:auth:sym:auth:app/user.rb:class:User:1");
  });

  it("loadService is lazy and caches: a second call does not refetch", async () => {
    await treeStore.loadService("auth");
    await treeStore.loadService("auth");
    const calls = (fetch as any).mock.calls.filter(([u]: [string]) => u.startsWith("/api/tree"));
    expect(calls.length).toBe(1);
  });

  it("aggregates unresolved counts up the folder path, exact-matches at the file level", async () => {
    await treeStore.loadService("auth");
    expect(treeStore.unresolvedCount("auth", "folder", "app")).toBe(2);
    expect(treeStore.unresolvedCount("auth", "file", "app/user.rb")).toBe(1);
    expect(treeStore.unresolvedCount("auth", "file", "app/other.rb")).toBe(1);
  });

  it("reveal loads the owning service, expands ancestors, and highlights the exact row", async () => {
    await treeStore.reveal("auth:app/user.rb:method:save:5");
    expect(treeStore.expanded().has("svc:auth")).toBe(true);
    expect(treeStore.expanded().has("svc:auth:folder:app")).toBe(true);
    expect(treeStore.expanded().has("svc:auth:file:app/user.rb")).toBe(true);
    expect(treeStore.expanded().has("svc:auth:sym:auth:app/user.rb:class:User:1")).toBe(true);
    expect(treeStore.highlightedKey()).toBe("svc:auth:sym:auth:app/user.rb:method:save:5");
  });

  it("reveal on an id with no tree representation loads the service and stops (no crash)", async () => {
    const before = treeStore.highlightedKey();
    await treeStore.reveal("auth:some/thing:http_handler:index:9");
    expect(treeStore.entryFor("auth").tree).toBeDefined();
    // Unfound ids leave the previous highlight untouched rather than clearing it.
    expect(treeStore.highlightedKey()).toBe(before);
  });

  it("graph_updated SSE invalidates every loaded service's cache", async () => {
    class FakeEventSource {
      onopen: (() => void) | null = null;
      onerror: (() => void) | null = null;
      onmessage: ((ev: MessageEvent) => void) | null = null;
      constructor(public url: string) {
        (FakeEventSource as any).last = this;
      }
      close() {}
    }
    const realES = (global as any).EventSource;
    (global as any).EventSource = FakeEventSource;

    await treeStore.loadService("auth");
    expect(treeStore.entryFor("auth").tree).toBeDefined();

    treeStore.start();
    connectionStore.start();
    (FakeEventSource as any).last.onmessage?.({ data: JSON.stringify({ type: "graph_updated" }) });

    expect(treeStore.entryFor("auth").tree).toBeUndefined();

    connectionStore.stop();
    (global as any).EventSource = realES;
  });
});
