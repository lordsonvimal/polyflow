import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { contextCopyStore } from "./contextCopy";
import { drawerStore } from "./drawer";
import { scopeStore } from "./scope";

function fakeFetch(routes: Record<string, unknown | { status: number; body: string }>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    const entry = routes[match];
    if (entry && typeof entry === "object" && "status" in (entry as object) && "body" in (entry as object)) {
      const e = entry as { status: number; body: string };
      return Promise.resolve({ ok: false, status: e.status, text: async () => e.body } as Response);
    }
    return Promise.resolve({ ok: true, json: async () => entry } as Response);
  });
}

const OK_RESPONSE = { markdown: "# Context: n1\n\nbody\n", tokens_estimate: 5, truncated: false, omitted: [] };

describe("contextCopyStore", () => {
  beforeEach(() => {
    drawerStore.setOpen(false);
    drawerStore.setActiveTab("unresolved");
    scopeStore.reset();
    contextCopyStore.setMode("viewed");
    contextCopyStore.setDepth(2);
    contextCopyStore.setSnippets(true);
    contextCopyStore.setMaxTokens(8000);
  });

  afterEach(() => vi.restoreAllMocks());

  it("copy() fetches the bundle, opens the drawer on the context tab, and clears loading", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/context/bundle": OK_RESPONSE });
    const promise = contextCopyStore.copy({ kind: "node", id: "n1" });
    expect(drawerStore.open()).toBe(true);
    expect(drawerStore.activeTab()).toBe("context");
    expect(contextCopyStore.loading()).toBe(true);
    await promise;
    expect(contextCopyStore.loading()).toBe(false);
    expect(contextCopyStore.result()).toEqual(OK_RESPONSE);
    expect(contextCopyStore.error()).toBeNull();
  });

  it("copy() records the bundle in recent, most-recent first, capped at 10", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/context/bundle": OK_RESPONSE });
    for (let i = 0; i < 12; i++) {
      await contextCopyStore.copy({ kind: "node", id: `n${i}` });
    }
    const recent = contextCopyStore.recent();
    expect(recent).toHaveLength(10);
    expect(recent[0].label).toBe("node n11");
  });

  it("copy() surfaces the UB.6 error verbatim, unwrapped from its JSON envelope", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/context/bundle": { status: 404, body: JSON.stringify({ error: "unknown id(s): missing1" }) },
    });
    await contextCopyStore.copy({ kind: "node", id: "missing1" });
    expect(contextCopyStore.error()).toBe("unknown id(s): missing1");
    expect(contextCopyStore.result()).toBeNull();
  });

  it("reopen() restores a recent bundle as the current result without refetching", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/context/bundle": OK_RESPONSE });
    await contextCopyStore.copy({ kind: "node", id: "n1" });
    const bundle = contextCopyStore.recent()[0];
    drawerStore.setActiveTab("unresolved");
    drawerStore.setOpen(false);

    const fetchSpy = ((globalThis as any).fetch = vi.fn());
    contextCopyStore.reopen(bundle);
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(contextCopyStore.result()).toEqual(OK_RESPONSE);
    expect(drawerStore.open()).toBe(true);
    expect(drawerStore.activeTab()).toBe("context");
  });

  it("refreshView() clears the error and resets the scope, per the stale-id contract", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/context/bundle": { status: 404, body: JSON.stringify({ error: "unknown id(s): x" }) },
    });
    await contextCopyStore.copy({ kind: "node", id: "x" });
    expect(contextCopyStore.error()).not.toBeNull();
    scopeStore.push({ kind: "service", service: "svc" });

    contextCopyStore.refreshView();
    expect(contextCopyStore.error()).toBeNull();
    expect(scopeStore.stack()).toEqual([{ kind: "overview" }]);
    expect(scopeStore.staleIdNotice()).toBe(true);
    scopeStore.dismissStaleIdNotice();
  });
});
