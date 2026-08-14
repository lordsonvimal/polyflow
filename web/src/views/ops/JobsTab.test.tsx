import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import JobsTab from "./JobsTab";
import { jobsStore, type Job } from "../../stores/jobs";

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

// Keyed by "METHOD pathname" (e.g. "GET /api/jobs", "POST /api/jobs") since
// GET (history) and POST (start) share the same path.
function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string, init?: RequestInit) => {
    const u = new URL(url, "http://localhost");
    const key = `${init?.method ?? "GET"} ${u.pathname}`;
    const entry = routes[key];
    if (entry === undefined) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => entry } as Response);
  });
}

describe("JobsTab", () => {
  let container: HTMLElement;

  beforeEach(() => {
    jobsStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    container.remove();
    jobsStore.reset();
    vi.restoreAllMocks();
  });

  it("fetches and renders job history on mount, incl. a failed job's error expander", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/jobs": { jobs: [job({ id: "j2", state: "failed", error: "boom", ended_at: new Date().toISOString() })] },
    });
    render(() => <JobsTab />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="jobs-history-row"]')).toHaveLength(1));
    expect(container.querySelector('[data-testid="jobs-history-error"]')).toBeFalsy();

    (container.querySelector('[data-testid="jobs-history-error-toggle"]') as HTMLElement).click();
    expect(container.querySelector('[data-testid="jobs-history-error"]')?.textContent).toBe("boom");
  });

  it("renders a running card with a progress bar and live log tail", async () => {
    (globalThis as any).fetch = fakeFetch({
      "POST /api/jobs": { job: job({ progress: { done: 4, total: 10 }, log_tail: ["parsing files…"] }) },
      "GET /api/jobs": { jobs: [] },
    });
    await jobsStore.startIndex(false);

    render(() => <JobsTab />, container);
    expect(container.querySelector('[data-testid="jobs-running-card"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="jobs-running-progress"]')?.textContent).toBe("4/10");
    expect(container.querySelector('[data-testid="jobs-log-tail"]')?.textContent).toContain("parsing files…");
  });

  it("cancel requires confirmation, then DELETEs the job", async () => {
    (globalThis as any).fetch = fakeFetch({ "POST /api/jobs": { job: job() }, "GET /api/jobs": { jobs: [] } });
    await jobsStore.startIndex(false);

    render(() => <JobsTab />, container);
    (container.querySelector('[data-testid="jobs-cancel"]') as HTMLElement).click();
    expect(container.querySelector('[data-testid="jobs-cancel-confirm"]')).toBeTruthy();

    const deleteSpy = ((globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: true, text: async () => "" } as Response)));
    (container.querySelector('[data-testid="jobs-cancel-confirm"]') as HTMLElement).click();
    await Promise.resolve();

    expect(deleteSpy).toHaveBeenCalledWith("/api/jobs/j1", expect.objectContaining({ method: "DELETE" }));
  });
});
