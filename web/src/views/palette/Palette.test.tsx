import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import Palette from "./Palette";
import { paletteStore } from "../../stores/palette";
import { layoutPrefs } from "../../stores/layoutPrefs";
import { scopeStore } from "../../stores/scope";
import { selectionStore } from "../../stores/selection";
import { treeStore } from "../../stores/tree";

// Flushes pending microtasks under fake timers (real setTimeout is stubbed out).
function flush() {
  return vi.advanceTimersByTimeAsync(0);
}

describe("Palette", () => {
  let container: HTMLElement;
  let deferred: Record<string, (body: unknown) => void>;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    localStorage.clear();
    paletteStore.close();
    layoutPrefs.setActivity("explore");
    treeStore.reset();
    scopeStore.reset();
    selectionStore.setSelection(null);
    deferred = {};
    (globalThis as any).fetch = vi.fn((url: string) => {
      const u = new URL(url, "http://localhost");
      const key = `${u.pathname}?${u.searchParams.get("q") ?? ""}`;
      return new Promise((resolve) => {
        deferred[key] = (body: unknown) => resolve({ ok: true, json: async () => body } as Response);
      });
    });
    container = document.createElement("div");
    document.body.appendChild(container);
    paletteStore.open();
    dispose = render(() => <Palette />, container);
  });

  afterEach(() => {
    // Un-disposed prior renders stay reactive on the paletteStore singleton
    // (isOpen/pendingQuery) and race the current test's instance — dispose
    // is required, not just DOM removal.
    dispose?.();
    container.remove();
    vi.useRealTimers();
  });

  function input(): HTMLInputElement {
    return container.querySelector('[data-testid="palette-input"]') as HTMLInputElement;
  }

  function type(v: string) {
    const el = input();
    el.value = v;
    el.dispatchEvent(new Event("input", { bubbles: true }));
  }

  function key(k: string) {
    input().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));
  }

  it("debounces network calls and discards a stale out-of-order response", async () => {
    vi.useFakeTimers();
    type("sync");
    await vi.advanceTimersByTimeAsync(100);
    expect(deferred["/api/graph/search?sync"]).toBeUndefined(); // still within the 150ms debounce window
    await vi.advanceTimersByTimeAsync(60);
    expect(deferred["/api/graph/search?sync"]).toBeDefined(); // "sync" request fired

    type("syncing");
    await vi.advanceTimersByTimeAsync(150); // "syncing" request fires

    // Resolve the newer ("syncing") request first — the fast response.
    deferred["/api/graph/search?syncing"]([
      { id: "n2", type: "function", label: "syncingFn", service: "svc", file: "a.rb", line: 1 },
    ]);
    deferred["/api/files?syncing"]({ files: [] });
    await flush();

    // Now resolve the older ("sync") request — arrives late, must be discarded.
    deferred["/api/graph/search?sync"]([
      { id: "n1", type: "function", label: "syncFn", service: "svc", file: "b.rb", line: 1 },
    ]);
    deferred["/api/files?sync"]({ files: [] });
    await flush();

    expect(container.textContent).toContain("syncingFn");
    expect(container.textContent).not.toContain("syncFn");
  });

  it("supports keyboard-only operation: ArrowDown/ArrowUp move highlight, Enter selects", async () => {
    vi.useFakeTimers();
    type("activity");
    await vi.advanceTimersByTimeAsync(150);

    key("ArrowDown");
    key("ArrowDown");
    key("Enter");

    expect(layoutPrefs.activity()).toBe("impact"); // 3rd activity command (index 2)
    expect(paletteStore.isOpen()).toBe(false); // Enter closes the palette
  });

  it("Escape closes the palette without selecting", async () => {
    vi.useFakeTimers();
    type("activity");
    await vi.advanceTimersByTimeAsync(150);

    key("Escape");

    expect(paletteStore.isOpen()).toBe(false);
    expect(layoutPrefs.activity()).toBe("explore"); // unchanged
  });

  it("Enter on a symbol result builds a depth-4 neighborhood scope, selects it, and reveals it in the tree", async () => {
    vi.useFakeTimers();
    const revealSpy = vi.spyOn(treeStore, "reveal").mockResolvedValue();
    type("createUser");
    await vi.advanceTimersByTimeAsync(150);

    deferred["/api/graph/search?createUser"]([
      { id: "auth:app/user.rb:method:createUser:10", label: "createUser", type: "method", service: "auth", file: "app/user.rb", line: 10, end_line: 15 },
    ]);
    deferred["/api/files?createUser"]({ files: [] });
    await flush();

    key("Enter");

    expect(scopeStore.stack().at(-1)).toEqual({ kind: "neighborhood", nodeId: "auth:app/user.rb:method:createUser:10", depth: 4 });
    expect(selectionStore.selection()).toEqual({ kind: "node", id: "auth:app/user.rb:method:createUser:10" });
    expect(revealSpy).toHaveBeenCalledWith("auth:app/user.rb:method:createUser:10");
    expect(paletteStore.isOpen()).toBe(false);
  });

  it("builds a depth-4 neighborhood scope for a symbol with no known file too (never a silent no-op)", async () => {
    vi.useFakeTimers();
    type("mystery");
    await vi.advanceTimersByTimeAsync(150);

    deferred["/api/graph/search?mystery"]([
      { id: "svc:synthetic:mystery", label: "mystery", type: "function", service: "svc", file: "", line: 0 },
    ]);
    deferred["/api/files?mystery"]({ files: [] });
    await flush();

    key("Enter");

    expect(scopeStore.stack().at(-1)).toEqual({ kind: "neighborhood", nodeId: "svc:synthetic:mystery", depth: 4 });
  });

  it("Enter on a service result pushes its service scope", async () => {
    vi.useFakeTimers();
    await flush(); // let Palette's onMount-triggered /api/stack fetch fire
    deferred["/api/stack?"]({ services: [{ name: "railssvc", language: "ruby", frameworks: [], files: 10 }] });
    await flush();

    type("rails");
    await vi.advanceTimersByTimeAsync(150);
    deferred["/api/graph/search?rails"]([]);
    deferred["/api/files?rails"]({ files: [] });
    await flush();

    expect(container.textContent).toContain("railssvc");

    key("Enter"); // no symbol/file hits for "rails" — the service row is first

    expect(scopeStore.stack().at(-1)).toEqual({ kind: "service", service: "railssvc" });
  });

  it("openWithQuery (UN.4 number click-navigate) pre-fills the search box and consumes the pending query once", async () => {
    vi.useFakeTimers();
    paletteStore.close();
    await flush();

    paletteStore.openWithQuery("kind:http_handler service:railssvc");
    await flush();

    expect(input().value).toBe("kind:http_handler service:railssvc");
    expect(paletteStore.pendingQuery()).toBeUndefined();

    // Re-opening later (no pending query set) must not replay the old text.
    key("Escape"); // full reset, including the query box
    paletteStore.open();
    await flush();
    expect(input().value).toBe("");
  });
});
