import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import Tree from "./Tree";
import { treeStore, type ApiTreeResult } from "../../stores/tree";
import { selectionStore } from "../../stores/selection";
import { scopeStore } from "../../stores/scope";
import { canvasElementsStore } from "../../stores/canvasElements";
import { drawerStore } from "../../stores/drawer";

function flush() {
  return vi.advanceTimersByTimeAsync(0);
}

function manyFilesTree(n: number): ApiTreeResult {
  return {
    service: "auth",
    tree: Array.from({ length: n }, (_, i) => ({
      kind: "file",
      name: `f${i}.rb`,
      path: `f${i}.rb`,
      node_id: `auth:f${i}.rb:file::0`,
      children: [],
    })),
    counts: { folders: 0, files: n, symbols: 0 },
  };
}

const SMALL_TREE: ApiTreeResult = {
  service: "auth",
  tree: [
    {
      kind: "file",
      name: "user.rb",
      path: "user.rb",
      node_id: "auth:user.rb:file::0",
      children: [
        { kind: "class", name: "User", node_id: "auth:user.rb:class:User:1", line: 1, end_line: 20, children: [] },
      ],
    },
  ],
  counts: { folders: 0, files: 1, symbols: 1 },
};

describe("Tree", () => {
  let container: HTMLElement;
  let dispose: () => void;

  function mockRoutes(routes: Record<string, unknown>) {
    (globalThis as any).fetch = vi.fn((url: string) => {
      const u = new URL(url, "http://localhost");
      const key = u.pathname + u.search;
      const match = Object.keys(routes).find((k) => key === k || key.startsWith(k));
      if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
      return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
    });
  }

  beforeEach(() => {
    vi.useFakeTimers();
    treeStore.reset();
    selectionStore.setSelection(null);
    selectionStore.setHoverTarget(null);
    scopeStore.reset();
    canvasElementsStore.setIds(new Set());
    drawerStore.setOpen(false);
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
    treeStore.stop();
    vi.useRealTimers();
  });

  function mount() {
    dispose = render(() => <Tree />, container);
  }

  function rows(): HTMLElement[] {
    return Array.from(container.querySelectorAll('[data-testid="tree-row"]'));
  }

  it("lists services eagerly but fetches a service's tree lazily on first expand", async () => {
    mockRoutes({
      "/api/stack": { services: [{ name: "auth", language: "ruby", frameworks: [], files: 1 }] },
      "/api/tree?service=auth": SMALL_TREE,
      "/api/unresolved?service=auth": { refs: [], total: 0, page: 1 },
    });
    mount();
    await flush();

    expect(rows()).toHaveLength(1);
    expect(rows()[0].textContent).toContain("auth");
    expect(fetch).not.toHaveBeenCalledWith(expect.stringContaining("/api/tree"), expect.anything());

    // Single click (300ms disambiguation window) on the service row expands + loads it.
    rows()[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    await flush();

    expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/api/tree?service=auth"), expect.anything());
    expect(rows().map((r) => r.dataset.kind)).toEqual(["service", "file"]);

    // A second expand does not refetch.
    const callsBefore = (fetch as any).mock.calls.length;
    rows()[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    rows()[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    await flush();
    expect((fetch as any).mock.calls.length).toBe(callsBefore);
  });

  it("virtualizes: a 300-file service only mounts a small window of rows, not all 300", async () => {
    mockRoutes({
      "/api/stack": { services: [{ name: "auth", language: "ruby", frameworks: [], files: 300 }] },
      "/api/tree?service=auth": manyFilesTree(300),
      "/api/unresolved?service=auth": { refs: [], total: 0, page: 1 },
    });
    mount();
    await flush();

    rows()[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    await flush();

    const rendered = rows().length;
    expect(rendered).toBeGreaterThan(1); // service row + some files
    expect(rendered).toBeLessThan(100); // nowhere near the full 300 files
  });

  it("canvas selection reveals and highlights the owning tree row, auto-expanding ancestors", async () => {
    mockRoutes({
      "/api/stack": { services: [{ name: "auth", language: "ruby", frameworks: [], files: 1 }] },
      "/api/tree?service=auth": SMALL_TREE,
      "/api/unresolved?service=auth": { refs: [], total: 0, page: 1 },
    });
    mount();
    await flush();

    // Simulate a canvas tap selecting the class node — the tree hasn't been
    // expanded/loaded by the user at all yet.
    selectionStore.setSelection({ kind: "node", id: "auth:user.rb:class:User:1" });
    await flush();

    const classRow = rows().find((r) => r.dataset.kind === "class");
    expect(classRow).toBeTruthy();
    expect(classRow!.className).toContain("bg-neutral-700");
  });

  it("selecting a tree row present on canvas just selects; absent from canvas it opens that scope", async () => {
    mockRoutes({
      "/api/stack": { services: [{ name: "auth", language: "ruby", frameworks: [], files: 1 }] },
      "/api/tree?service=auth": SMALL_TREE,
      "/api/unresolved?service=auth": { refs: [], total: 0, page: 1 },
    });
    mount();
    await flush();
    rows()[0].dispatchEvent(new MouseEvent("click", { bubbles: true })); // expand service
    await vi.advanceTimersByTimeAsync(300);
    await flush();
    rows()
      .find((r) => r.dataset.kind === "file")!
      .querySelector('[data-testid="tree-row-toggle"]')!
      .dispatchEvent(new MouseEvent("click", { bubbles: true })); // expand file (chevron, not select)
    await flush();

    const classRow = () => rows().find((r) => r.dataset.kind === "class")!;
    const stackLenBefore = scopeStore.stack().length;

    // Not on canvas — clicking it must open a scope, never silently no-op.
    classRow().dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    expect(selectionStore.selection()).toEqual({ kind: "node", id: "auth:user.rb:class:User:1" });
    expect(scopeStore.stack().length).toBe(stackLenBefore + 1);

    // Now it IS on canvas — selecting again must not push another scope.
    canvasElementsStore.setIds(new Set(["auth:user.rb:class:User:1"]));
    const stackLenAfter = scopeStore.stack().length;
    classRow().dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    expect(scopeStore.stack().length).toBe(stackLenAfter);
  });

  it("orphan-under-file symbols (no incoming contains edge) render nested under their file", async () => {
    const tree: ApiTreeResult = {
      service: "auth",
      tree: [
        {
          kind: "file",
          name: "user.rb",
          path: "user.rb",
          node_id: "auth:user.rb:file::0",
          children: [
            // A Ruby class fallback-attached under its file (see BuildTree's
            // orphan handling), with its own contains child.
            {
              kind: "class",
              name: "User",
              node_id: "auth:user.rb:class:User:1",
              line: 1,
              end_line: 20,
              children: [{ kind: "method", name: "save", node_id: "auth:user.rb:method:save:5", line: 5, end_line: 6, children: [] }],
            },
          ],
        },
      ],
      counts: { folders: 0, files: 1, symbols: 2 },
    };
    mockRoutes({
      "/api/stack": { services: [{ name: "auth", language: "ruby", frameworks: [], files: 1 }] },
      "/api/tree?service=auth": tree,
      "/api/unresolved?service=auth": { refs: [], total: 0, page: 1 },
    });
    mount();
    await flush();
    rows()[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    await flush();

    treeStore.expandKeys(["svc:auth:file:user.rb", "svc:auth:sym:auth:user.rb:class:User:1"]);
    await flush();

    expect(rows().map((r) => r.dataset.kind)).toEqual(["service", "file", "class", "method"]);
  });

  it("unresolved badge aggregates up the path and opens the drawer pre-filtered on click", async () => {
    const tree: ApiTreeResult = {
      service: "auth",
      tree: [{ kind: "folder", name: "app", path: "app", children: [] }],
      counts: { folders: 1, files: 0, symbols: 0 },
    };
    mockRoutes({
      "/api/stack": { services: [{ name: "auth", language: "ruby", frameworks: [], files: 1 }] },
      "/api/tree?service=auth": tree,
      "/api/unresolved?service=auth": {
        refs: [
          { service: "auth", file: "app/a.rb", line: 1, name: "x", kind: "call" },
          { service: "auth", file: "app/b.rb", line: 2, name: "y", kind: "call" },
        ],
        total: 2,
        page: 1,
      },
    });
    mount();
    await flush();
    rows()[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    await flush();

    const badge = container.querySelector('[data-testid="unresolved-badge"]') as HTMLButtonElement;
    expect(badge.textContent).toContain("2");

    badge.click();
    expect(drawerStore.open()).toBe(true);
    expect(drawerStore.unresolvedFilter()).toEqual({ service: "auth", path: "app" });
  });
});
