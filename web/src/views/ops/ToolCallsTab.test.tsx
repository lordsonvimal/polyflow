import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import ToolCallsTab from "./ToolCallsTab";
import { toolCallsStore, type ToolCallRow } from "../../stores/toolcalls";
import { scopeStore } from "../../stores/scope";

function row(overrides: Partial<ToolCallRow> = {}): ToolCallRow {
  return {
    id: 1,
    ts: new Date().toISOString(),
    source: "mcp",
    tool: "search",
    params: JSON.stringify({ q: "needle" }),
    duration_ms: 10,
    status: "ok",
    error: "",
    result: JSON.stringify({ hits: ["needle here"] }),
    result_bytes: 30,
    result_truncated: false,
    ...overrides,
  };
}

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string, init?: RequestInit) => {
    const u = new URL(url, "http://localhost");
    const decodedPath = decodeURIComponent(u.pathname);
    const key = `${init?.method ?? "GET"} ${decodedPath}`;
    const entry = routes[key] ?? routes[decodedPath];
    if (entry === undefined) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => entry, text: async () => JSON.stringify(entry) } as Response);
  });
}

describe("ToolCallsTab", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    toolCallsStore.reset();
    scopeStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
    toolCallsStore.reset();
    vi.restoreAllMocks();
  });

  it("loads and renders rows on mount, filter click issues source=cli", async () => {
    const fetchMock = fakeFetch({ "GET /api/toolcalls": { calls: [row()], total: 1, page: 1 } });
    (globalThis as any).fetch = fetchMock;
    dispose = render(() => <ToolCallsTab />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="toolcalls-row"]')).toHaveLength(1));

    (container.querySelector('[data-testid="toolcalls-filter-source-cli"]') as HTMLElement).click();
    await vi.waitFor(() => {
      const last = fetchMock.mock.calls.at(-1)![0] as string;
      const u = new URL(last, "http://localhost");
      expect(u.searchParams.get("source")).toBe("cli");
    });
  });

  it("expands a row into Input/Output panes with highlight marks", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/toolcalls": { calls: [row()], total: 1, page: 1 } });
    dispose = render(() => <ToolCallsTab />, container);
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="toolcalls-row"]')).toHaveLength(1));

    const input = container.querySelector('[data-testid="toolcalls-filter-q"]') as HTMLInputElement;
    input.value = "needle";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await new Promise((r) => setTimeout(r, 300));

    (container.querySelector('[data-testid="toolcalls-row-summary"]') as HTMLElement).click();
    await vi.waitFor(() => expect(container.querySelector('[data-testid="toolcalls-row-expanded"]')).toBeTruthy());

    const marks = container.querySelectorAll('[data-testid="toolcalls-input-json"] mark, [data-testid="toolcalls-output-json"] mark');
    expect(marks.length).toBeGreaterThan(0);
  });

  it("renders the truncated banner with the honest download-limitation copy", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/toolcalls": {
        calls: [row({ result_truncated: true, result: "x".repeat(100), result_bytes: 210_000 })],
        total: 1,
        page: 1,
      },
    });
    dispose = render(() => <ToolCallsTab />, container);
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="toolcalls-row"]')).toHaveLength(1));
    (container.querySelector('[data-testid="toolcalls-row-summary"]') as HTMLElement).click();

    await vi.waitFor(() => expect(container.querySelector('[data-testid="toolcalls-truncated-banner"]')).toBeTruthy());
    expect(container.querySelector('[data-testid="toolcalls-truncated-banner"]')?.textContent).toContain("not retained");
  });

  it("duration color thresholds: amber above 1s, red above 5s", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/toolcalls": {
        calls: [row({ id: 1, duration_ms: 200 }), row({ id: 2, duration_ms: 2000 }), row({ id: 3, duration_ms: 8000 })],
        total: 3,
        page: 1,
      },
    });
    dispose = render(() => <ToolCallsTab />, container);
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="toolcalls-row"]')).toHaveLength(3));

    const durations = container.querySelectorAll('[data-testid="toolcalls-row-duration"]');
    expect(durations[0].className).toContain("text-neutral-400");
    expect(durations[1].className).toContain("text-amber-400");
    expect(durations[2].className).toContain("text-red-400");
  });

  it("clear-all requires confirmation, then DELETEs and shows the empty state", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/toolcalls": { calls: [row()], total: 1, page: 1 },
      "DELETE /api/toolcalls": { deleted: 1 },
    });
    dispose = render(() => <ToolCallsTab />, container);
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="toolcalls-row"]')).toHaveLength(1));

    (container.querySelector('[data-testid="toolcalls-clear"]') as HTMLElement).click();
    expect(container.querySelector('[data-testid="toolcalls-clear-confirm"]')).toBeTruthy();

    const deleteSpy = ((globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: true, text: async () => "" } as Response)));
    (container.querySelector('[data-testid="toolcalls-clear-confirm"]') as HTMLElement).click();
    await vi.waitFor(() => expect(container.querySelector('[data-testid="toolcalls-empty"]')).toBeTruthy());
    expect(deleteSpy).toHaveBeenCalledWith("/api/toolcalls", expect.objectContaining({ method: "DELETE" }));
  });

  it("jump-to-node resolves an id embedded in the output and pushes the file scope", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/toolcalls": {
        calls: [row({ result: JSON.stringify({ target: "svc-a:orders.go:Handle:12" }) })],
        total: 1,
        page: 1,
      },
      "/api/node/svc-a:orders.go:Handle:12": { node: { id: "svc-a:orders.go:Handle:12", service: "svc-a", file: "orders.go" } },
    });
    dispose = render(() => <ToolCallsTab />, container);
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="toolcalls-row"]')).toHaveLength(1));
    (container.querySelector('[data-testid="toolcalls-row-summary"]') as HTMLElement).click();

    await vi.waitFor(() => expect(container.querySelector('[data-testid="toolcalls-jump-link"]')).toBeTruthy());
    (container.querySelector('[data-testid="toolcalls-jump-link"]') as HTMLElement).click();
    expect(scopeStore.stack().at(-1)).toEqual({ kind: "file", service: "svc-a", path: "orders.go" });
  });
});
