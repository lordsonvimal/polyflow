import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import Palette from "./Palette";
import { paletteStore } from "../../stores/palette";
import { layoutPrefs } from "../../stores/layoutPrefs";

// Flushes pending microtasks under fake timers (real setTimeout is stubbed out).
function flush() {
  return vi.advanceTimersByTimeAsync(0);
}

describe("Palette", () => {
  let container: HTMLElement;
  let deferred: Record<string, (body: unknown) => void>;

  beforeEach(() => {
    localStorage.clear();
    paletteStore.close();
    layoutPrefs.setActivity("explore");
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
    render(() => <Palette />, container);
  });

  afterEach(() => {
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
    expect(fetch).not.toHaveBeenCalled(); // still within the 150ms debounce window
    await vi.advanceTimersByTimeAsync(60);
    expect(fetch).toHaveBeenCalled(); // "sync" request fired

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
});
