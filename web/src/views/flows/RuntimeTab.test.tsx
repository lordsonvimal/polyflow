import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import RuntimeTab from "./RuntimeTab";
import { captureStore } from "../../stores/capture";
import { runtimeStore } from "../../stores/runtime";
import { runtimeViewStore } from "../../stores/runtimeView";
import { notificationsStore } from "../../stores/notifications";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string, init?: RequestInit) => {
    const u = new URL(url, "http://localhost");
    const key = `${init?.method ?? "GET"} ${u.pathname}${u.search}`;
    const exact = routes[key];
    if (exact !== undefined) return Promise.resolve({ ok: true, json: async () => exact } as Response);
    // fall back to path-only match (query-agnostic) for convenience
    const pathKey = `${init?.method ?? "GET"} ${u.pathname}`;
    const byPath = Object.keys(routes).find((k) => k.startsWith(pathKey));
    if (byPath) return Promise.resolve({ ok: true, json: async () => routes[byPath] } as Response);
    return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
  });
}

describe("RuntimeTab", () => {
  let container: HTMLElement;

  beforeEach(() => {
    captureStore.reset();
    runtimeStore.reset();
    runtimeViewStore.setSelectedSession(null);
    notificationsStore.clear();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    captureStore.stopPolling();
    captureStore.reset();
    runtimeStore.reset();
    runtimeViewStore.setSelectedSession(null);
    container.remove();
    vi.restoreAllMocks();
  });

  it("renders the session list from the capture status fixture and defaults to the first session", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/capture/status": {
        active: [],
        sessions: [{ Name: "s1", StartedAt: "now", SpanCount: 3, Age: "1s old" }],
      },
      "GET /api/runtime/flows": { spans: [], flow_records: [{ Kind: "http_call", Key: "GET /orders", FromService: "web", ToService: "api", Causality: "parent_child" }], ledger: [] },
      "GET /api/runtime/coverage": {
        coverage: { Rows: [{ Kind: "http_call", Total: 2, Verified: 1, Candidate: 1, Gap: 0, Pct: 50 }], TotalChannels: 2, VerifiedChannels: 1, CandidateChannels: 1, GapChannels: 0, LedgerByReason: {}, ObservedOnlyGaps: [] },
      },
    });
    render(() => <RuntimeTab />, container);
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));

    const select = container.querySelector('[data-testid="runtime-session-select"]') as HTMLSelectElement;
    expect(select.value).toBe("s1");
    expect(container.querySelectorAll('[data-testid="runtime-flow-row"]')).toHaveLength(1);
  });

  it("renders the inline ingest ledger, never hiding it even when empty", async () => {
    runtimeViewStore.setSelectedSession("s1");
    (globalThis as any).fetch = fakeFetch({
      "GET /api/capture/status": { active: [], sessions: [{ Name: "s1", StartedAt: "now", SpanCount: 1, Age: "1s old" }] },
      "GET /api/runtime/flows": { spans: [], flow_records: [], ledger: [] },
      "GET /api/runtime/coverage": {
        coverage: { Rows: [], TotalChannels: 0, VerifiedChannels: 0, CandidateChannels: 0, GapChannels: 0, LedgerByReason: {}, ObservedOnlyGaps: [] },
      },
    });
    render(() => <RuntimeTab />, container);
    await new Promise((r) => setTimeout(r, 0));

    expect(container.textContent).toContain("Every observed span mapped cleanly.");
  });

  it("shows unmapped ledger entries with their reason", async () => {
    runtimeViewStore.setSelectedSession("s1");
    (globalThis as any).fetch = fakeFetch({
      "GET /api/capture/status": { active: [], sessions: [{ Name: "s1", StartedAt: "now", SpanCount: 1, Age: "1s old" }] },
      "GET /api/runtime/flows": {
        spans: [],
        flow_records: [],
        ledger: [{ Session: "s1", TraceID: "t1", SpanID: "sp1", Service: "unknown", Reason: "unknown_service" }],
      },
      "GET /api/runtime/coverage": {
        coverage: { Rows: [], TotalChannels: 0, VerifiedChannels: 0, CandidateChannels: 0, GapChannels: 0, LedgerByReason: { unknown_service: 1 }, ObservedOnlyGaps: [] },
      },
    });
    render(() => <RuntimeTab />, container);
    await new Promise((r) => setTimeout(r, 0));

    const rows = container.querySelectorAll('[data-testid="runtime-ledger-row"]');
    expect(rows).toHaveLength(1);
    expect(rows[0].textContent).toContain("unknown_service");
  });

  it("observed-only gap rows expose 'propose contract rule', fetching and displaying the YAML", async () => {
    runtimeViewStore.setSelectedSession("s1");
    (globalThis as any).fetch = fakeFetch({
      "GET /api/capture/status": { active: [], sessions: [{ Name: "s1", StartedAt: "now", SpanCount: 1, Age: "1s old" }] },
      "GET /api/runtime/flows": { spans: [], flow_records: [], ledger: [] },
      "GET /api/runtime/coverage": {
        coverage: {
          Rows: [],
          TotalChannels: 0,
          VerifiedChannels: 0,
          CandidateChannels: 0,
          GapChannels: 1,
          LedgerByReason: {},
          ObservedOnlyGaps: [{ Kind: "http_call", Key: "GET /new", From: "web", To: "api" }],
        },
      },
      "GET /api/reconcile/propose": { filename: "http-call-get-new.yaml", content: "proposed: true\nkind: http_call\n" },
    });
    render(() => <RuntimeTab />, container);
    await new Promise((r) => setTimeout(r, 0));

    const proposeBtn = container.querySelector('[data-testid="runtime-gap-propose"]') as HTMLElement;
    expect(proposeBtn).toBeTruthy();
    proposeBtn.click();
    await new Promise((r) => setTimeout(r, 0));

    const yaml = container.querySelector('[data-testid="runtime-gap-proposal-yaml"]');
    expect(yaml?.textContent).toContain("proposed: true");
  });

  it("Import OTLP dump… uploads a file via captureStore.ingestDump", async () => {
    runtimeViewStore.setSelectedSession("s1");
    const fetchSpy = ((globalThis as any).fetch = fakeFetch({
      "GET /api/capture/status": { active: [], sessions: [{ Name: "s1", StartedAt: "now", SpanCount: 1, Age: "1s old" }] },
      "GET /api/runtime/flows": { spans: [], flow_records: [], ledger: [] },
      "GET /api/runtime/coverage": {
        coverage: { Rows: [], TotalChannels: 0, VerifiedChannels: 0, CandidateChannels: 0, GapChannels: 0, LedgerByReason: {}, ObservedOnlyGaps: [] },
      },
      "POST /api/capture/ingest": { session: "s1", span_count: 2, fusion_hint: "run index to fuse this evidence into the graph" },
    }));
    render(() => <RuntimeTab />, container);
    await new Promise((r) => setTimeout(r, 0));

    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File(["{}"], "dump.json", { type: "application/json" });
    Object.defineProperty(fileInput, "files", { value: [file] });
    fileInput.dispatchEvent(new Event("change", { bubbles: true }));
    await new Promise((r) => setTimeout(r, 0));

    const ingestCall = fetchSpy.mock.calls.find(([url, init]: [string, RequestInit]) => url === "/api/capture/ingest" && init?.method === "POST");
    expect(ingestCall).toBeTruthy();
  });
});
