import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import TopBar from "./TopBar";
import { layoutPrefs } from "../stores/layoutPrefs";
import { pinboardStore } from "../stores/pinboard";
import { scopeStore } from "../stores/scope";
import { selectionStore } from "../stores/selection";
import { jobsStore, type Job } from "../stores/jobs";
import { drawerStore } from "../stores/drawer";

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
