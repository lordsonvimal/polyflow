import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { toolCallsStore, type ToolCallRow } from "./toolcalls";
import { connectionStore } from "./connection";

function row(overrides: Partial<ToolCallRow> = {}): ToolCallRow {
  return {
    id: 1,
    ts: new Date().toISOString(),
    source: "mcp",
    tool: "search",
    params: JSON.stringify({ q: "foo" }),
    duration_ms: 10,
    status: "ok",
    error: "",
    result: JSON.stringify({ hits: [] }),
    result_bytes: 20,
    result_truncated: false,
    ...overrides,
  };
}

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string, init?: RequestInit) => {
    const u = new URL(url, "http://localhost");
    const key = `${init?.method ?? "GET"} ${u.pathname}`;
    const entry = routes[key];
    if (entry === undefined) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => entry, text: async () => JSON.stringify(entry) } as Response);
  });
}

// Minimal fake EventSource so SSE payloads can be driven deterministically —
// mirrors connection.test.ts's fake (jsdom has no real EventSource).
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeEventSource.instances.push(this);
  }
  close() {
    this.closed = true;
  }
}

function send(payload: object) {
  const es = FakeEventSource.instances.at(-1);
  es?.onmessage?.({ data: JSON.stringify(payload) });
}

describe("toolCallsStore", () => {
  const realES = (global as any).EventSource;

  beforeEach(() => {
    toolCallsStore.reset();
    FakeEventSource.instances = [];
    (global as any).EventSource = FakeEventSource;
  });

  afterEach(() => {
    connectionStore.stop();
    toolCallsStore.reset();
    (global as any).EventSource = realES;
    vi.restoreAllMocks();
  });

  it("fetches page 1 on loadInitial with default (unfiltered) params", async () => {
    const fetchMock = fakeFetch({ "GET /api/toolcalls": { calls: [row()], total: 1, page: 1 } });
    (globalThis as any).fetch = fetchMock;
    toolCallsStore.loadInitial();
    await vi.waitFor(() => expect(toolCallsStore.rows()).toHaveLength(1));
    const calledUrl = new URL(fetchMock.mock.calls[0][0], "http://localhost");
    expect(calledUrl.searchParams.get("source")).toBeNull();
    expect(calledUrl.searchParams.get("page")).toBe("1");
  });

  it("setFilters(source=cli) issues a request with source=cli exactly", async () => {
    const fetchMock = fakeFetch({ "GET /api/toolcalls": { calls: [], total: 0, page: 1 } });
    (globalThis as any).fetch = fetchMock;
    toolCallsStore.setFilters({ source: "cli" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const calledUrl = new URL(fetchMock.mock.calls.at(-1)![0], "http://localhost");
    expect(calledUrl.searchParams.get("source")).toBe("cli");
  });

  it("prepends live tool_call events matching the current filter, ignores non-matching", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/toolcalls": { calls: [], total: 0, page: 1 } });
    toolCallsStore.loadInitial();
    await vi.waitFor(() => expect(toolCallsStore.loading()).toBe(false));
    toolCallsStore.setFilters({ source: "cli" });
    await vi.waitFor(() => expect(toolCallsStore.loading()).toBe(false));

    connectionStore.start();
    send({ type: "tool_call", call: row({ id: 5, source: "mcp" }) }); // filtered out
    send({ type: "tool_call", call: row({ id: 6, source: "cli" }) }); // matches

    expect(toolCallsStore.rows().map((r) => r.id)).toEqual([6]);
    expect(toolCallsStore.total()).toBe(1);
  });

  it("pause buffers live events and resume flushes them with a count", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/toolcalls": { calls: [], total: 0, page: 1 } });
    toolCallsStore.loadInitial();
    await vi.waitFor(() => expect(toolCallsStore.loading()).toBe(false));

    toolCallsStore.togglePause();
    connectionStore.start();
    send({ type: "tool_call", call: row({ id: 7 }) });
    send({ type: "tool_call", call: row({ id: 8 }) });

    expect(toolCallsStore.rows()).toHaveLength(0);
    expect(toolCallsStore.bufferedCount()).toBe(2);

    toolCallsStore.togglePause();
    expect(toolCallsStore.rows().map((r) => r.id)).toEqual([8, 7]);
    expect(toolCallsStore.bufferedCount()).toBe(0);
  });

  it("tool_call_evicted removes rows without a refetch", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/toolcalls": { calls: [row({ id: 1 }), row({ id: 2 })], total: 2, page: 1 } });
    toolCallsStore.loadInitial();
    await vi.waitFor(() => expect(toolCallsStore.rows()).toHaveLength(2));

    const fetchSpy = vi.fn();
    (globalThis as any).fetch = fetchSpy;
    connectionStore.start();
    send({ type: "tool_call_evicted", ids: [1] });

    expect(toolCallsStore.rows().map((r) => r.id)).toEqual([2]);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("clearAll empties rows and total", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/toolcalls": { calls: [row()], total: 1, page: 1 },
      "DELETE /api/toolcalls": { deleted: 1 },
    });
    toolCallsStore.loadInitial();
    await vi.waitFor(() => expect(toolCallsStore.rows()).toHaveLength(1));

    await toolCallsStore.clearAll();
    expect(toolCallsStore.rows()).toHaveLength(0);
    expect(toolCallsStore.total()).toBe(0);
  });

  it("loadMore appends the next page and stops once total is reached", async () => {
    (globalThis as any).fetch = vi.fn((url: string) => {
      const u = new URL(url, "http://localhost");
      const page = u.searchParams.get("page");
      if (page === "1") return Promise.resolve({ ok: true, json: async () => ({ calls: [row({ id: 2 })], total: 2, page: 1 }) } as Response);
      return Promise.resolve({ ok: true, json: async () => ({ calls: [row({ id: 1 })], total: 2, page: 2 }) } as Response);
    });
    toolCallsStore.loadInitial();
    await vi.waitFor(() => expect(toolCallsStore.rows()).toHaveLength(1));
    toolCallsStore.loadMore();
    await vi.waitFor(() => expect(toolCallsStore.rows()).toHaveLength(2));
  });

  it("marks a gap divider when a reconnect fetch returns a non-contiguous newest id", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/toolcalls": { calls: [row({ id: 5 })], total: 5, page: 1 } });
    toolCallsStore.loadInitial();
    await vi.waitFor(() => expect(toolCallsStore.rows()).toHaveLength(1));

    (globalThis as any).fetch = fakeFetch({ "GET /api/toolcalls": { calls: [row({ id: 9 }), row({ id: 5 })], total: 9, page: 1 } });
    connectionStore.start();
    FakeEventSource.instances[0].onopen?.(); // establish the initial connection first
    FakeEventSource.instances[0].onerror?.(); // simulate a drop before the reconnect
    connectionStore.reconnectNow();
    FakeEventSource.instances.at(-1)!.onopen?.();

    await vi.waitFor(() => expect(toolCallsStore.rows().map((r) => r.id)).toEqual([9, 5]));
    expect(toolCallsStore.gapBeforeId()).toBe(5);
  });
});
