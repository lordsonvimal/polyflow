import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { savedViewsStore } from "./savedViews";
import { scopeStore, encodeViewState, DEFAULT_STATE } from "./scope";

function fakeFetch(routes: Record<string, unknown | { status: number; body: string }>) {
  return vi.fn((url: string, init?: RequestInit) => {
    const u = new URL(url, "http://localhost");
    const key = `${init?.method ?? "GET"} ${u.pathname}`;
    const match = Object.keys(routes).find((k) => key === k) ?? Object.keys(routes).find((k) => k === u.pathname);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    const entry = routes[match];
    if (entry && typeof entry === "object" && "status" in (entry as object) && "body" in (entry as object)) {
      const e = entry as { status: number; body: string };
      return Promise.resolve({ ok: false, status: e.status, text: async () => e.body, json: async () => JSON.parse(e.body) } as Response);
    }
    return Promise.resolve({ ok: true, json: async () => entry, text: async () => JSON.stringify(entry) } as Response);
  });
}

describe("savedViewsStore", () => {
  beforeEach(() => {
    scopeStore.reset();
    savedViewsStore.reset();
  });

  afterEach(() => vi.restoreAllMocks());

  it("save → list → apply round-trips the current ViewState", async () => {
    const state = encodeViewState({ ...DEFAULT_STATE, stack: [{ kind: "overview" }] });
    const view = { id: 1, name: "fleet seam", state, created_at: "2026-08-18T00:00:00Z" };

    (globalThis as any).fetch = fakeFetch({
      "POST /api/views": { view },
      "GET /api/views": { views: [view] },
    });

    const saved = await savedViewsStore.save("fleet seam");
    expect(saved?.name).toBe("fleet seam");

    await savedViewsStore.list();
    expect(savedViewsStore.views()).toHaveLength(1);
    expect(savedViewsStore.views()[0].name).toBe("fleet seam");

    await savedViewsStore.apply(view);
    expect(scopeStore.viewState().stack).toEqual([{ kind: "overview" }]);
  });

  it("save() surfaces a 409 as a friendly conflict notification, not a thrown error", async () => {
    (globalThis as any).fetch = fakeFetch({
      "POST /api/views": { status: 409, body: JSON.stringify({ error: "a saved view with this name already exists" }) },
    });
    const result = await savedViewsStore.save("dup");
    expect(result).toBeNull();
  });

  it("apply() falls back to overview and notifies when the view's anchor node is stale (404)", async () => {
    const state = encodeViewState({ ...DEFAULT_STATE, stack: [{ kind: "overview" }, { kind: "neighborhood", nodeId: "gone", depth: 2 }] });
    const view = { id: 2, name: "stale view", state, created_at: "2026-08-18T00:00:00Z" };

    (globalThis as any).fetch = fakeFetch({
      "GET /api/node/gone": { status: 404, body: "not found" },
    });

    await savedViewsStore.apply(view);
    expect(scopeStore.viewState().stack).toEqual(DEFAULT_STATE.stack);
    expect(scopeStore.staleIdNotice()).toBe(true);
    scopeStore.dismissStaleIdNotice();
  });

  it("apply() reports corrupted state without touching the scope stack", async () => {
    const before = scopeStore.viewState();
    await savedViewsStore.apply({ id: 3, name: "bad", state: "not-valid-base64!!", created_at: "x" });
    expect(scopeStore.viewState()).toBe(before);
  });

  it("remove() drops the view from the list on success", async () => {
    const state = encodeViewState(DEFAULT_STATE);
    const view = { id: 4, name: "to remove", state, created_at: "x" };
    (globalThis as any).fetch = fakeFetch({
      "POST /api/views": { view },
      "DELETE /api/views/4": { status: "deleted" },
    });
    await savedViewsStore.save("to remove");
    await savedViewsStore.remove(4);
    expect(savedViewsStore.views().find((v) => v.id === 4)).toBeUndefined();
  });
});
