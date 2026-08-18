import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import TopBar from "./TopBar";
import { layoutPrefs } from "../stores/layoutPrefs";
import { pinboardStore } from "../stores/pinboard";
import { scopeStore } from "../stores/scope";
import { selectionStore } from "../stores/selection";
import { jobsStore, type Job } from "../stores/jobs";
import { drawerStore } from "../stores/drawer";
import { canvasRefStore } from "../stores/canvasRef";
import { savedViewsStore } from "../stores/savedViews";
import { captureStore } from "../stores/capture";
import { runtimeViewStore } from "../stores/runtimeView";
import { notificationsStore } from "../stores/notifications";

describe("TopBar theme toggle", () => {
  let container: HTMLElement;
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.classList.remove("dark");
    container = document.createElement("div");
    document.body.appendChild(container);
  });
  afterEach(() => { container.remove(); });

  it("toggle adds dark class and persists", () => {
    layoutPrefs.setTheme("light");
    render(() => <TopBar />, container);

    // find the theme toggle button (☾ or ☀)
    const btns = Array.from(container.querySelectorAll("button")) as HTMLButtonElement[];
    const toggleBtn = btns.find(b => b.textContent === "☾" || b.textContent === "☀")!;
    expect(toggleBtn).not.toBeUndefined();

    toggleBtn.click();
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(localStorage.getItem("pf:theme")).toBe("dark");
  });

  it("toggle removes dark class when already dark", () => {
    layoutPrefs.setTheme("dark");
    document.documentElement.classList.add("dark");
    render(() => <TopBar />, container);

    const btns = Array.from(container.querySelectorAll("button")) as HTMLButtonElement[];
    const toggleBtn = btns.find(b => b.textContent === "☾" || b.textContent === "☀")!;
    toggleBtn.click();
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(localStorage.getItem("pf:theme")).toBe("light");
  });
});

// UF.7: pin tray
describe("TopBar pin tray", () => {
  let container: HTMLElement;
  beforeEach(() => {
    scopeStore.reset();
    selectionStore.setSelection(null);
    container = document.createElement("div");
    document.body.appendChild(container);
  });
  afterEach(() => {
    container.remove();
    scopeStore.reset();
  });

  it("is hidden with no pins, appears with one, badges without 'view as flow lane'", () => {
    render(() => <TopBar />, container);
    expect(container.querySelector('[data-testid="pin-tray"]')).toBeFalsy();

    pinboardStore.pin({ id: "a", label: "Publisher" });
    expect(container.querySelector('[data-testid="pin-tray"]')).toBeTruthy();
    expect(container.querySelectorAll('[data-testid="pin-chip"]')).toHaveLength(1);
    expect(container.querySelector('[data-testid="pin-tray-view-as-lane"]')).toBeFalsy();
  });

  it("shows 'View as flow lane' once 2+ pins are set, and pushes a pins flow scope", () => {
    pinboardStore.pin({ id: "a", label: "Publisher" });
    pinboardStore.pin({ id: "b", label: "Consumer" });
    render(() => <TopBar />, container);

    const btn = container.querySelector('[data-testid="pin-tray-view-as-lane"]') as HTMLElement;
    expect(btn).toBeTruthy();
    btn.click();
    expect(scopeStore.stack().at(-1)).toEqual({ kind: "flow", flow: { kind: "pins", ids: ["a", "b"] } });
  });

  it("chip × unpins; [clear all] empties the tray", () => {
    pinboardStore.pin({ id: "a", label: "Publisher" });
    pinboardStore.pin({ id: "b", label: "Consumer" });
    render(() => <TopBar />, container);

    const chips = () => container.querySelectorAll('[data-testid="pin-chip"]');
    expect(chips()).toHaveLength(2);
    (chips()[0].querySelector("button:last-child") as HTMLElement).click();
    expect(pinboardStore.pins().map((p) => p.id)).toEqual(["b"]);

    (container.querySelector('[data-testid="pin-tray-clear"]') as HTMLElement).click();
    expect(pinboardStore.pins()).toEqual([]);
  });

  it("clicking a chip's label selects that node", () => {
    pinboardStore.pin({ id: "a", label: "Publisher" });
    render(() => <TopBar />, container);

    const label = container.querySelector('[data-testid="pin-chip"] button') as HTMLElement;
    label.click();
    expect(selectionStore.selection()).toEqual({ kind: "node", id: "a" });
  });
});

// UO.0: Index ▸ button state machine
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

describe("TopBar Index button", () => {
  let container: HTMLElement;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    drawerStore.setOpen(false);
    drawerStore.setActiveTab("context");
    jobsStore.reset();
  });

  afterEach(() => {
    container.remove();
    vi.restoreAllMocks();
    jobsStore.reset();
  });

  it("idle: clicking Index ▸ POSTs a non-full index job", async () => {
    const fetchSpy = ((globalThis as any).fetch = vi.fn(() =>
      Promise.resolve({ ok: true, json: async () => ({ job: job() }) } as Response),
    ));
    render(() => <TopBar />, container);

    const btn = container.querySelector('[data-testid="index-button"]') as HTMLElement;
    expect(btn).toBeTruthy();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(fetchSpy).toHaveBeenCalled();
    const call = fetchSpy.mock.calls.find(([url]: [string]) => url === "/api/jobs")!;
    expect(JSON.parse(call[1].body)).toEqual({ kind: "index", args: { full: false } });
  });

  it("dropdown 'Full re-index' sends args.full: true", async () => {
    const fetchSpy = ((globalThis as any).fetch = vi.fn(() =>
      Promise.resolve({ ok: true, json: async () => ({ job: job() }) } as Response),
    ));
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="index-menu-toggle"]') as HTMLElement).click();
    const fullBtn = container.querySelector('[data-testid="index-full-reindex"]') as HTMLElement;
    expect(fullBtn).toBeTruthy();
    fullBtn.click();
    await new Promise((r) => setTimeout(r, 0));

    const call = fetchSpy.mock.calls.find(([url]: [string]) => url === "/api/jobs")!;
    expect(JSON.parse(call[1].body).args).toEqual({ full: true });
  });

  it("running: renders a progress ring + done/total instead of the idle button", async () => {
    (globalThis as any).fetch = vi.fn(() =>
      Promise.resolve({ ok: true, json: async () => ({ job: job({ progress: { done: 2, total: 8 } }) }) } as Response),
    );
    render(() => <TopBar />, container);
    expect(container.querySelector('[data-testid="index-button"]')).toBeTruthy();

    await jobsStore.startIndex(false);

    expect(container.querySelector('[data-testid="index-button"]')).toBeFalsy();
    expect(container.querySelector('[data-testid="index-progress-button"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="index-progress-text"]')?.textContent).toBe("2/8");
  });

  it("clicking the progress button while running opens the Jobs drawer tab", async () => {
    const fetchSpy = ((globalThis as any).fetch = vi.fn(() =>
      Promise.resolve({ ok: true, json: async () => ({ job: job({ progress: { done: 3, total: 10 } }) }) } as Response),
    ));
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="index-button"]') as HTMLElement).click();
    await new Promise((r) => setTimeout(r, 0));
    void fetchSpy;

    const progressBtn = container.querySelector('[data-testid="index-progress-button"]') as HTMLElement;
    expect(progressBtn).toBeTruthy();
    expect(container.querySelector('[data-testid="index-progress-text"]')?.textContent).toBe("3/10");

    progressBtn.click();
    expect(drawerStore.open()).toBe(true);
    expect(drawerStore.activeTab()).toBe("jobs");
  });
});

// UO.5: Share menu — export, copy link, save view
function fakeCy() {
  const elements = {
    boundingBox: () => ({ w: 400, h: 300 }),
    jsons: () => [{ group: "nodes", data: { id: "n1" } }],
  };
  return {
    svg: vi.fn(() => "<svg></svg>"),
    png: vi.fn(() => "data:image/png;base64,AAAA"),
    elements: vi.fn(() => elements),
  } as any;
}

describe("TopBar Share menu", () => {
  let container: HTMLElement;

  beforeEach(() => {
    scopeStore.reset();
    canvasRefStore.set(null);
    container = document.createElement("div");
    document.body.appendChild(container);
    (globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: true, json: async () => ({ nodes: 0, edges: 0 }) } as Response));
    // jsdom doesn't implement the Blob URL APIs downloadBlob/downloadText use.
    URL.createObjectURL = vi.fn(() => "blob:fake");
    URL.revokeObjectURL = vi.fn();
  });

  afterEach(() => {
    container.remove();
    vi.restoreAllMocks();
    canvasRefStore.set(null);
    scopeStore.reset();
  });

  it("Copy link writes the current view's hash URL to the clipboard", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.assign(navigator, { clipboard: { writeText } });
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="share-menu-toggle"]') as HTMLElement).click();
    (container.querySelector('[data-testid="share-copy-link"]') as HTMLElement).click();
    await Promise.resolve();

    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText.mock.calls[0][0]).toContain("#v=");
  });

  it("export buttons are disabled with no live canvas", () => {
    render(() => <TopBar />, container);
    (container.querySelector('[data-testid="share-menu-toggle"]') as HTMLElement).click();

    expect((container.querySelector('[data-testid="share-export-png"]') as HTMLButtonElement).disabled).toBe(true);
    expect((container.querySelector('[data-testid="share-export-svg"]') as HTMLButtonElement).disabled).toBe(true);
    expect((container.querySelector('[data-testid="share-export-json"]') as HTMLButtonElement).disabled).toBe(true);
  });

  it("Export SVG calls cy.svg() when a canvas is live", () => {
    const cy = fakeCy();
    canvasRefStore.set(cy);
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="share-menu-toggle"]') as HTMLElement).click();
    (container.querySelector('[data-testid="share-export-svg"]') as HTMLElement).click();

    expect(cy.svg).toHaveBeenCalledTimes(1);
    expect(cy.png).not.toHaveBeenCalled();
  });

  it("Export PNG calls cy.png() (not cy.svg()) when a canvas is live", () => {
    const cy = fakeCy();
    canvasRefStore.set(cy);
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="share-menu-toggle"]') as HTMLElement).click();
    (container.querySelector('[data-testid="share-export-png"]') as HTMLElement).click();

    expect(cy.png).toHaveBeenCalledTimes(1);
    expect(cy.svg).not.toHaveBeenCalled();
  });

  it("Export PNG falls back to SVG when PNG rasterization fails", () => {
    const cy = fakeCy();
    cy.png = vi.fn(() => "not-a-data-url");
    canvasRefStore.set(cy);
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="share-menu-toggle"]') as HTMLElement).click();
    (container.querySelector('[data-testid="share-export-png"]') as HTMLElement).click();

    expect(cy.png).toHaveBeenCalledTimes(1);
    expect(cy.svg).toHaveBeenCalledTimes(1);
  });

  it("Export JSON calls cy.elements().jsons()", () => {
    const cy = fakeCy();
    canvasRefStore.set(cy);
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="share-menu-toggle"]') as HTMLElement).click();
    (container.querySelector('[data-testid="share-export-json"]') as HTMLElement).click();

    expect(cy.elements).toHaveBeenCalled();
  });

  it("Export Mermaid fetches /api/export/mermaid at the level matching the current scope", async () => {
    scopeStore.push({ kind: "service", service: "svc" });
    const fetchSpy = ((globalThis as any).fetch = vi.fn(() => Promise.resolve(new Response("flowchart LR\n", { status: 200 }))));
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="share-menu-toggle"]') as HTMLElement).click();
    (container.querySelector('[data-testid="share-export-mermaid"]') as HTMLElement).click();
    await new Promise((r) => setTimeout(r, 0));

    const call = fetchSpy.mock.calls.find(([url]: [string]) => String(url).startsWith("/api/export/mermaid"));
    expect(call).toBeTruthy();
    expect(String(call![0])).toContain("level=service");
  });

  it("star button opens the save-view dialog; submitting POSTs /api/views", async () => {
    const fetchSpy = ((globalThis as any).fetch = vi.fn((url: string, init?: RequestInit) => {
      if (String(url) === "/api/views" && init?.method === "POST") {
        return Promise.resolve({ ok: true, json: async () => ({ view: { id: 1, name: "my view", state: "s", created_at: "x" } }) } as Response);
      }
      return Promise.resolve({ ok: true, json: async () => ({}) } as Response);
    }));
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="save-view-button"]') as HTMLElement).click();
    expect(container.querySelector('[data-testid="save-view-overlay"]')).toBeTruthy();

    const input = container.querySelector('[data-testid="save-view-name-input"]') as HTMLInputElement;
    input.value = "my view";
    input.dispatchEvent(new Event("input", { bubbles: true }));

    (container.querySelector('[data-testid="save-view-submit"]') as HTMLElement).click();
    await new Promise((r) => setTimeout(r, 0));

    const call = fetchSpy.mock.calls.find(([url, init]: [string, RequestInit]) => url === "/api/views" && init?.method === "POST");
    expect(call).toBeTruthy();
    expect(JSON.parse((call![1] as RequestInit).body as string).name).toBe("my view");
    expect(container.querySelector('[data-testid="save-view-overlay"]')).toBeFalsy();
    savedViewsStore.reset();
  });
});

// UO.6: Record control state machine (idle/starting/active/stopping)
function fakeCaptureFetch(routes: Record<string, unknown | { status: number; body: string }>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: true, json: async () => ({ active: [], sessions: [] }) } as Response);
    const entry = routes[match];
    if (entry && typeof entry === "object" && "status" in (entry as object) && "body" in (entry as object)) {
      const e = entry as { status: number; body: string };
      return Promise.resolve({ ok: false, status: e.status, text: async () => e.body } as Response);
    }
    return Promise.resolve({ ok: true, json: async () => entry } as Response);
  });
}

describe("TopBar Record control", () => {
  let container: HTMLElement;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    captureStore.reset();
    notificationsStore.clear();
  });

  afterEach(() => {
    captureStore.stopPolling();
    captureStore.reset();
    runtimeViewStore.setTab("catalog");
    container.remove();
    vi.restoreAllMocks();
  });

  it("idle: clicking Record opens the session dialog prefilled with ui-<date>", () => {
    (globalThis as any).fetch = fakeCaptureFetch({});
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="record-button"]') as HTMLElement).click();
    const input = container.querySelector('[data-testid="record-session-name-input"]') as HTMLInputElement;
    expect(input).toBeTruthy();
    expect(input.value).toMatch(/^ui-\d{4}-\d{2}-\d{2}$/);
  });

  it("submitting the dialog starts a capture and shows the pulsing active control", async () => {
    (globalThis as any).fetch = fakeCaptureFetch({
      "/api/capture/start": { session: "ui-test", status: "active", http_port: 4318, grpc_port: 4317 },
      "/api/capture/status": { active: [{ session: "ui-test", since: "now", spans_received: 0, http_port: 4318, grpc_port: 4317 }], sessions: [] },
    });
    render(() => <TopBar />, container);

    (container.querySelector('[data-testid="record-button"]') as HTMLElement).click();
    (container.querySelector('[data-testid="record-dialog-submit"]') as HTMLElement).click();
    await new Promise((r) => setTimeout(r, 0));

    expect(container.querySelector('[data-testid="record-active-button"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="record-pulse"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="record-dialog-overlay"]')).toBeFalsy();
  });

  it("active: clicking shows a stop confirm; confirming stops and prompts to fuse", async () => {
    (globalThis as any).fetch = fakeCaptureFetch({
      "/api/capture/start": { session: "ui-test", status: "active", http_port: 4318, grpc_port: 4317 },
      "/api/capture/status": { active: [{ session: "ui-test", since: "now", spans_received: 5, http_port: 4318, grpc_port: 4317 }], sessions: [] },
      "/api/capture/stop": { session: "ui-test", finalized: true, fusion_hint: "run index to fuse this evidence into the graph" },
    });
    render(() => <TopBar />, container);

    await captureStore.start("ui-test");
    await new Promise((r) => setTimeout(r, 0));

    (container.querySelector('[data-testid="record-active-button"]') as HTMLElement).click();
    expect(container.querySelector('[data-testid="record-stop-confirm"]')).toBeTruthy();

    (globalThis as any).fetch = fakeCaptureFetch({
      "/api/capture/status": { active: [], sessions: [{ Name: "ui-test", StartedAt: "now", SpanCount: 5, Age: "1s old" }] },
      "/api/capture/stop": { session: "ui-test", finalized: true, fusion_hint: "run index to fuse this evidence into the graph" },
    });
    (container.querySelector('[data-testid="record-stop-confirm"]') as HTMLElement).click();
    await new Promise((r) => setTimeout(r, 0));

    expect(container.querySelector('[data-testid="record-active-button"]')).toBeFalsy();
  });

  it("stop -> fuse prompt toast wires 'Fuse now' to the index job and opens the Runtime tab", async () => {
    const startIndexSpy = vi.spyOn(jobsStore, "startIndex").mockResolvedValue();
    (globalThis as any).fetch = fakeCaptureFetch({
      "/api/capture/status": { active: [{ session: "ui-test", since: "now", spans_received: 5, http_port: 4318, grpc_port: 4317 }], sessions: [] },
    });
    render(() => <TopBar />, container);
    await captureStore.refreshStatus();

    (globalThis as any).fetch = fakeCaptureFetch({
      "/api/capture/status": { active: [], sessions: [{ Name: "ui-test", StartedAt: "now", SpanCount: 5, Age: "1s old" }] },
      "/api/capture/stop": { session: "ui-test", finalized: true, fusion_hint: "run index to fuse this evidence into the graph" },
    });
    await captureStore.stop("ui-test");
    await new Promise((r) => setTimeout(r, 0));

    const toast = notificationsStore.toasts().find((t) => t.message.includes("Fuse into graph now?"));
    expect(toast).toBeTruthy();
    expect(toast!.action?.label).toBe("Fuse now");
    toast!.action!.onClick();
    expect(startIndexSpy).toHaveBeenCalledWith(false);
    expect(runtimeViewStore.tab()).toBe("runtime");
  });
});
