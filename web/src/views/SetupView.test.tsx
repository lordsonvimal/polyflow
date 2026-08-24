import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import SetupView from "./SetupView";
import { setupStore } from "../stores/setup";
import { jobsStore } from "../stores/jobs";

function fakeFetch(routes: Record<string, unknown | { status: number; body: string }>) {
  return vi.fn((url: string, init?: RequestInit) => {
    const u = new URL(url, "http://localhost");
    const key = `${init?.method ?? "GET"} ${u.pathname}`;
    const match = routes[key] ?? routes[u.pathname];
    if (match === undefined) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    if (match && typeof match === "object" && "status" in (match as object) && "body" in (match as object)) {
      const e = match as { status: number; body: string };
      return Promise.resolve({ ok: false, status: e.status, text: async () => e.body, json: async () => JSON.parse(e.body) } as Response);
    }
    return Promise.resolve({ ok: true, status: 200, json: async () => match, text: async () => JSON.stringify(match) } as Response);
  });
}

const DISCOVERED = { name: "ws", version: "1", services: [{ name: "api", path: "./api", language: "go" }] };

describe("SetupView", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    setupStore.reset();
    jobsStore.reset();
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  function mount(routes: Record<string, unknown>) {
    (globalThis as any).fetch = fakeFetch(routes);
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <SetupView />, container);
  }

  it("discovers services then shows them for confirmation", async () => {
    mount({
      "POST /api/jobs": { job: { id: "j1", kind: "init", state: "running" } },
      "GET /api/jobs/j1": { id: "j1", kind: "init", state: "succeeded", result: JSON.stringify(DISCOVERED) },
    });

    (container.querySelector('[data-testid="setup-discover-button"]') as HTMLElement).click();

    await vi.waitFor(() => expect(container.querySelector('[data-testid="setup-step-confirm"]')).toBeTruthy());
    expect(container.textContent).toContain("api");
  });

  it("applies the confirmed config then moves to the index step", async () => {
    mount({
      "POST /api/jobs": { job: { id: "j1", kind: "init", state: "running" } },
      "GET /api/jobs/j1": { id: "j1", kind: "init", state: "succeeded", result: JSON.stringify(DISCOVERED) },
      "POST /api/setup/apply": { path: "/ws/polyflow.yml", ok: true },
      "GET /api/setup/status": { needs_config: false, needs_index: true },
    });

    (container.querySelector('[data-testid="setup-discover-button"]') as HTMLElement).click();
    await vi.waitFor(() => expect(container.querySelector('[data-testid="setup-confirm-button"]')).toBeTruthy());

    (container.querySelector('[data-testid="setup-confirm-button"]') as HTMLElement).click();

    await vi.waitFor(() => expect(container.querySelector('[data-testid="setup-step-index"]')).toBeTruthy());
  });

  it("lists known registry workspaces and reloads after selecting one", async () => {
    const reload = vi.fn();
    Object.defineProperty(window, "location", { value: { ...window.location, reload }, writable: true });

    mount({
      "GET /api/setup/registry": {
        entries: [{ service: "api", local_path: "/repos/api", indexed_at: "2026-08-20T00:00:00Z" }],
      },
      "POST /api/setup/select": { restarting: true },
      "GET /api/setup/status": { needs_config: false, needs_index: false },
    });

    await vi.waitFor(() => expect(container.querySelector('[data-testid="setup-registry-entry"]')).toBeTruthy());
    expect(container.textContent).toContain("/repos/api");

    (container.querySelector('[data-testid="setup-registry-open-button"]') as HTMLElement).click();

    await vi.waitFor(() => expect(reload).toHaveBeenCalled(), { timeout: 3000 });
  });

  it("surfaces a select error without reloading", async () => {
    const reload = vi.fn();
    Object.defineProperty(window, "location", { value: { ...window.location, reload }, writable: true });

    mount({
      "GET /api/setup/registry": { entries: [{ service: "api", local_path: "/repos/api" }] },
      "POST /api/setup/select": { status: 500, body: "boom" },
    });

    await vi.waitFor(() => expect(container.querySelector('[data-testid="setup-registry-entry"]')).toBeTruthy());
    (container.querySelector('[data-testid="setup-registry-open-button"]') as HTMLElement).click();

    await vi.waitFor(() => expect(container.querySelector('[data-testid="setup-select-error"]')).toBeTruthy());
    expect(reload).not.toHaveBeenCalled();
  });
});
