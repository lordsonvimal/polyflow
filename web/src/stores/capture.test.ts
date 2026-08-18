import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { captureStore } from "./capture";
import { connectionStore } from "./connection";
import { notificationsStore } from "./notifications";
import { jobsStore } from "./jobs";

class FakeEventSource {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  constructor(public url: string) {
    (FakeEventSource as any).last = this;
  }
  close() {}
}

function emit(payload: Record<string, unknown>) {
  (FakeEventSource as any).last.onmessage?.({ data: JSON.stringify(payload) });
}

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

describe("captureStore", () => {
  const realES = (global as any).EventSource;

  beforeEach(() => {
    (global as any).EventSource = FakeEventSource;
    connectionStore.start();
    notificationsStore.clear();
    jobsStore.reset();
    captureStore.reset();
  });

  afterEach(() => {
    connectionStore.stop();
    (global as any).EventSource = realES;
    vi.restoreAllMocks();
  });

  it("start posts session/ports and flips state to active", async () => {
    const fetchSpy = ((globalThis as any).fetch = fakeFetch({
      "/api/capture/start": { session: "s1", status: "active", http_port: 4318, grpc_port: 4317 },
      "/api/capture/status": { active: [{ session: "s1", since: "now", spans_received: 0, http_port: 4318, grpc_port: 4317 }], sessions: [] },
    }));
    const ok = await captureStore.start("s1", 4318, 4317);
    expect(ok).toBe(true);
    expect(fetchSpy).toHaveBeenCalled();
    const [, init] = fetchSpy.mock.calls[0];
    expect(JSON.parse(init.body)).toEqual({ session: "s1", http_port: 4318, grpc_port: 4317 });
    expect(captureStore.captureState()).toBe("active");
    expect(captureStore.activeSessions()[0]?.session).toBe("s1");
    captureStore.stopPolling();
  });

  it("409 port conflict surfaces an error toast and stays idle", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/capture/start": { status: 409, body: JSON.stringify({ error: "port 4318 in use", port: 4318 }) },
    });
    const ok = await captureStore.start("s1");
    expect(ok).toBe(false);
    expect(captureStore.captureState()).toBe("idle");
    expect(notificationsStore.toasts().some((t) => t.kind === "error" && t.message.includes("4318"))).toBe(true);
  });

  it("capture_progress SSE updates the matching active session's span count", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/capture/start": { session: "s1", status: "active", http_port: 4318, grpc_port: 4317 },
      "/api/capture/status": { active: [{ session: "s1", since: "now", spans_received: 0, http_port: 4318, grpc_port: 4317 }], sessions: [] },
    });
    await captureStore.start("s1");
    emit({ type: "capture_progress", session: "s1", spans_received: 7 });
    expect(captureStore.activeSessions()[0]?.spans_received).toBe(7);
    captureStore.stopPolling();
  });

  it("stop finalizes the session and sets a fuse prompt with the span count", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/capture/start": { session: "s1", status: "active", http_port: 4318, grpc_port: 4317 },
      "/api/capture/status": { active: [], sessions: [{ Name: "s1", StartedAt: "now", SpanCount: 12, Age: "1s old" }] },
      "/api/capture/stop": { session: "s1", finalized: true, fusion_hint: "run index to fuse this evidence into the graph" },
    });
    await captureStore.start("s1");
    await captureStore.stop("s1");
    expect(captureStore.fusePrompt()).toEqual({ session: "s1", spanCount: 12 });
    expect(captureStore.captureState()).toBe("idle");
    captureStore.stopPolling();
  });

  it("fuseNow dismisses the prompt and starts the index job", async () => {
    const startIndexSpy = vi.spyOn(jobsStore, "startIndex").mockResolvedValue();
    (globalThis as any).fetch = fakeFetch({
      "/api/capture/start": { session: "s1", status: "active", http_port: 4318, grpc_port: 4317 },
      "/api/capture/status": { active: [], sessions: [{ Name: "s1", StartedAt: "now", SpanCount: 3, Age: "1s old" }] },
      "/api/capture/stop": { session: "s1", finalized: true, fusion_hint: "x" },
    });
    await captureStore.start("s1");
    await captureStore.stop("s1");
    captureStore.fuseNow();
    expect(captureStore.fusePrompt()).toBeNull();
    expect(startIndexSpy).toHaveBeenCalledWith(false);
    captureStore.stopPolling();
  });

  it("ingestDump posts multipart form data and shows a success toast", async () => {
    const fetchSpy = ((globalThis as any).fetch = fakeFetch({
      "/api/capture/ingest": { session: "dump1", span_count: 4, fusion_hint: "run index to fuse this evidence into the graph" },
      "/api/capture/status": { active: [], sessions: [] },
    }));
    const file = new File(["{}"], "dump.json", { type: "application/json" });
    await captureStore.ingestDump(file);
    expect(fetchSpy).toHaveBeenCalled();
    const [, init] = fetchSpy.mock.calls[0];
    expect(init.body).toBeInstanceOf(FormData);
    expect(notificationsStore.toasts().some((t) => t.kind === "success" && t.message === "Ingested 4 spans")).toBe(true);
    expect(captureStore.fusePrompt()).toEqual({ session: "dump1", spanCount: 4 });
  });

  it("refreshStatus discovers a CLI-started session without this tab calling start, and it is stoppable", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/capture/status": { active: [{ session: "cli1", since: "now", spans_received: 2, http_port: 4318, grpc_port: 4317 }], sessions: [] },
    });
    await captureStore.refreshStatus();
    expect(captureStore.captureState()).toBe("active");
    expect(captureStore.activeSessions()[0]?.session).toBe("cli1");
    expect(captureStore.uiSession()).toBeNull(); // this tab never called start()

    (globalThis as any).fetch = fakeFetch({
      "/api/capture/status": { active: [], sessions: [{ Name: "cli1", StartedAt: "now", SpanCount: 2, Age: "1s old" }] },
      "/api/capture/stop": { session: "cli1", finalized: true, fusion_hint: "run index to fuse this evidence into the graph" },
    });
    await captureStore.stop("cli1");
    expect(captureStore.fusePrompt()?.session).toBe("cli1");
    expect(captureStore.captureState()).toBe("idle");
  });
});
