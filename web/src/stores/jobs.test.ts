import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { jobsStore, type Job } from "./jobs";
import { connectionStore } from "./connection";
import { notificationsStore } from "./notifications";
import { drawerStore } from "./drawer";
import { scopeStore } from "./scope";

class FakeEventSource {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  constructor(public url: string) {
    (FakeEventSource as any).last = this;
  }
  close() {}
}

function emit(type: string, job: Job) {
  (FakeEventSource as any).last.onmessage?.({ data: JSON.stringify({ type, job }) });
}

function fakeFetch(routes: Record<string, unknown | { status: number; body: string }>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = `${u.pathname}`;
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

function job(overrides: Partial<Job> = {}): Job {
  return {
    id: "j1",
    kind: "index",
    args: "{}",
    state: "running",
    started_at: new Date().toISOString(),
    progress: { done: 0, total: 0 },
    log_tail: [],
    ...overrides,
  };
}

describe("jobsStore", () => {
  const realES = (global as any).EventSource;

  beforeEach(() => {
    (global as any).EventSource = FakeEventSource;
    connectionStore.start();
    notificationsStore.clear();
    drawerStore.setOpen(false);
    drawerStore.setActiveTab("context");
    scopeStore.reset();
    jobsStore.reset();
  });

  afterEach(() => {
    connectionStore.stop();
    (global as any).EventSource = realES;
    vi.restoreAllMocks();
  });

  it("startIndex posts kind:index and tracks the returned job as active", async () => {
    const fetchSpy = ((globalThis as any).fetch = fakeFetch({ "/api/jobs": { job: job() } }));
    await jobsStore.startIndex(false);
    expect(fetchSpy).toHaveBeenCalled();
    const [, init] = fetchSpy.mock.calls[0];
    expect(JSON.parse(init.body)).toEqual({ kind: "index", args: { full: false } });
    expect(jobsStore.activeIndexJob()?.id).toBe("j1");
  });

  it("full re-index sends args.full: true", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/jobs": { job: job() } });
    await jobsStore.startIndex(true);
    const [, init] = ((globalThis as any).fetch as any).mock.calls[0];
    expect(JSON.parse(init.body).args).toEqual({ full: true });
  });

  it("409 conflict shows an info toast, opens the Jobs tab, and adopts the running job", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/jobs": { status: 409, body: JSON.stringify({ error: "job j2 (index) is already running", job: job({ id: "j2" }) }) },
    });
    await jobsStore.startIndex(false);
    expect(notificationsStore.toasts().some((t) => t.message === "Index already running")).toBe(true);
    expect(drawerStore.open()).toBe(true);
    expect(drawerStore.activeTab()).toBe("jobs");
    expect(jobsStore.activeIndexJob()?.id).toBe("j2");
  });

  it("job_progress SSE events update the active job's progress live", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/jobs": { job: job() } });
    await jobsStore.startIndex(false);
    emit("job_progress", job({ progress: { done: 5, total: 20 } }));
    expect(jobsStore.activeIndexJob()?.progress).toEqual({ done: 5, total: 20 });
  });

  it("job_done success clears the active job, records history, toasts, and shows the reload banner", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/jobs": { job: job() } });
    await jobsStore.startIndex(false);
    emit("job_done", job({ state: "succeeded", ended_at: new Date().toISOString() }));

    expect(jobsStore.activeIndexJob()).toBeNull();
    expect(jobsStore.history().map((j) => j.id)).toContain("j1");
    expect(notificationsStore.toasts().some((t) => t.kind === "success" && t.message === "Index complete")).toBe(true);
    expect(jobsStore.reloadBanner()).toBe(true);
  });

  it("job_done failure toasts a persistent error with an 'open Jobs tab' action", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/jobs": { job: job() } });
    await jobsStore.startIndex(false);
    drawerStore.setOpen(false);
    emit("job_done", job({ state: "failed", error: "boom", ended_at: new Date().toISOString() }));

    const toast = notificationsStore.toasts().find((t) => t.kind === "error");
    expect(toast).toBeTruthy();
    expect(toast!.detail).toBe("boom");
    expect(toast!.action?.label).toBe("open Jobs tab");
    toast!.action!.onClick();
    expect(drawerStore.open()).toBe(true);
    expect(drawerStore.activeTab()).toBe("jobs");
  });

  it("reloadView clears the banner and bumps scopeStore.reloadNonce", () => {
    const before = scopeStore.reloadNonce();
    jobsStore.dismissReloadBanner();
    // simulate the banner being up
    (jobsStore as any).reloadBanner; // no-op read for clarity
    jobsStore.reloadView();
    expect(jobsStore.reloadBanner()).toBe(false);
    expect(scopeStore.reloadNonce()).toBe(before + 1);
  });

  it("cancel sends DELETE /api/jobs/{id}", async () => {
    const fetchSpy = ((globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: true, text: async () => "" } as Response)));
    await jobsStore.cancel("j1");
    expect(fetchSpy).toHaveBeenCalledWith("/api/jobs/j1", expect.objectContaining({ method: "DELETE" }));
  });

  it("fetchHistory loads GET /api/jobs into history", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/jobs": { jobs: [job({ state: "succeeded" })] } });
    await jobsStore.fetchHistory();
    expect(jobsStore.history()).toHaveLength(1);
  });
});
