import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import SavedViewsPanel from "./SavedViewsPanel";
import { savedViewsStore } from "../../stores/savedViews";
import { scopeStore, encodeViewState, DEFAULT_STATE } from "../../stores/scope";
import { getMenuItems } from "../../interaction/ContextMenu";

describe("SavedViewsPanel", () => {
  let container: HTMLElement;

  beforeEach(() => {
    savedViewsStore.reset();
    scopeStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    container.remove();
    vi.restoreAllMocks();
    savedViewsStore.reset();
    scopeStore.reset();
  });

  it("shows an empty state with no saved views", async () => {
    (globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: true, json: async () => ({ views: [] }) } as Response));
    render(() => <SavedViewsPanel />, container);
    await new Promise((r) => setTimeout(r, 0));
    expect(container.textContent).toContain("No saved views yet");
  });

  it("lists saved views and applies one on click", async () => {
    const view = { id: 1, name: "fleet seam", state: encodeViewState({ ...DEFAULT_STATE, stack: [{ kind: "overview" }] }), created_at: "2026-08-18T00:00:00Z" };
    (globalThis as any).fetch = vi.fn((url: string) => {
      if (String(url) === "/api/views") return Promise.resolve({ ok: true, json: async () => ({ views: [view] }) } as Response);
      if (String(url).startsWith("/api/node/")) return Promise.resolve({ ok: true, json: async () => ({}) } as Response);
      return Promise.resolve({ ok: true, json: async () => ({}) } as Response);
    });
    render(() => <SavedViewsPanel />, container);
    await new Promise((r) => setTimeout(r, 0));

    const row = container.querySelector('[data-testid="saved-view-row"]') as HTMLElement;
    expect(row.textContent).toContain("fleet seam");

    row.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(scopeStore.viewState().stack).toEqual([{ kind: "overview" }]);
  });

  it("right-click delete removes the view via the store", async () => {
    const view = { id: 2, name: "to delete", state: encodeViewState(DEFAULT_STATE), created_at: "x" };
    (globalThis as any).fetch = vi.fn((url: string, init?: RequestInit) => {
      if (String(url) === "/api/views") return Promise.resolve({ ok: true, json: async () => ({ views: [view] }) } as Response);
      if (String(url) === "/api/views/2" && init?.method === "DELETE") {
        return Promise.resolve({ ok: true, json: async () => ({ status: "deleted" }) } as Response);
      }
      return Promise.resolve({ ok: true, json: async () => ({}) } as Response);
    });
    render(() => <SavedViewsPanel />, container);
    await new Promise((r) => setTimeout(r, 0));

    const row = container.querySelector('[data-testid="saved-view-row"]') as HTMLElement;
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }));

    const deleteItem = getMenuItems().find((i) => i.label === "Delete");
    expect(deleteItem).toBeTruthy();
    deleteItem!.handler();
    await new Promise((r) => setTimeout(r, 0));

    expect(savedViewsStore.views().find((v) => v.id === 2)).toBeUndefined();
  });
});
