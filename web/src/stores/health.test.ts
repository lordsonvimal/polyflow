import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { healthStore, type HealthData } from "./health";
import { connectionStore } from "./connection";

class FakeEventSource {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  constructor(public url: string) {
    (FakeEventSource as any).last = this;
  }
  close() {}
}

function emit(type: string) {
  (FakeEventSource as any).last.onmessage?.({ data: JSON.stringify({ type }) });
}

function fixture(overrides: Partial<HealthData> = {}): HealthData {
  return {
    index: {
      indexed_at: "2026-08-18T00:00:00Z",
      schema_version: "31",
      nodes: 100,
      edges: 200,
      parse_errors: 0,
      parse_error_list: [],
    },
    coverage: { verified: 10, candidate: 5, observed_only_gap: 2, conflicting: 1 },
    unresolved_total: 3,
    unresolved_by_kind: { call: 2, import: 1 },
    eval: { present: false },
    trust: { measured: false },
    ...overrides,
  };
}

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const entry = routes[u.pathname];
    if (!entry) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => entry } as Response);
  });
}

describe("healthStore", () => {
  const realES = (global as any).EventSource;

  beforeEach(() => {
    (global as any).EventSource = FakeEventSource;
    connectionStore.start();
    healthStore.reset();
  });

  afterEach(() => {
    connectionStore.stop();
    (global as any).EventSource = realES;
    vi.restoreAllMocks();
  });

  it("loads /api/health into data()", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/health": fixture() });
    await healthStore.load();
    expect(healthStore.data()?.index.nodes).toBe(100);
    expect(healthStore.error()).toBeNull();
  });

  it("surfaces a fetch error without throwing", async () => {
    (globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: false, status: 500, text: async () => "boom" } as Response));
    await healthStore.load();
    expect(healthStore.data()).toBeNull();
    expect(healthStore.error()).toBeTruthy();
  });

  it("refetches on graph_updated SSE", async () => {
    const fetchSpy = ((globalThis as any).fetch = fakeFetch({ "/api/health": fixture() }));
    await healthStore.load();
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    emit("graph_updated");
    await Promise.resolve();
    await Promise.resolve();
    expect(fetchSpy).toHaveBeenCalledTimes(2);
  });

  it("refetches on job_done SSE", async () => {
    const fetchSpy = ((globalThis as any).fetch = fakeFetch({ "/api/health": fixture() }));
    await healthStore.load();
    emit("job_done");
    await Promise.resolve();
    await Promise.resolve();
    expect(fetchSpy).toHaveBeenCalledTimes(2);
  });

  it("does not refetch on unrelated SSE events", async () => {
    const fetchSpy = ((globalThis as any).fetch = fakeFetch({ "/api/health": fixture() }));
    await healthStore.load();
    emit("tool_call");
    await Promise.resolve();
    expect(fetchSpy).toHaveBeenCalledTimes(1);
  });
});
