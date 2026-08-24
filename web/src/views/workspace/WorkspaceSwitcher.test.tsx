import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import WorkspaceSwitcher from "./WorkspaceSwitcher";
import { setupStore } from "../../stores/setup";

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

describe("WorkspaceSwitcher", () => {
  let container: HTMLElement;

  beforeEach(() => {
    setupStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    container.remove();
    setupStore.reset();
    vi.restoreAllMocks();
  });

  it("shows an empty state when the registry has no known workspaces", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/setup/registry": { entries: [] } });
    render(() => <WorkspaceSwitcher />, container);

    await vi.waitFor(() => expect(container.querySelector('[data-testid="workspace-switcher-empty"]')).toBeTruthy());
  });

  it("marks the currently-open workspace instead of offering to reopen it", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/setup/registry": {
        entries: [
          { service: "api", local_path: "/repos/api" },
          { service: "web", local_path: "/repos/web" },
        ],
      },
    });
    await setupStore.checkStatus(); // populates status() from the same fake fetch's default 404 -> caught, stays null
    render(() => <WorkspaceSwitcher />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="workspace-switcher-row"]')).toHaveLength(2));
    // Neither entry matches an unset current path, so both offer "Open".
    expect(container.querySelectorAll('[data-testid="workspace-switcher-open"]')).toHaveLength(2);
  });

  it("selecting a workspace posts to /api/setup/select then reloads once the new process answers", async () => {
    const reload = vi.fn();
    Object.defineProperty(window, "location", { value: { ...window.location, reload }, writable: true });

    const routes: Record<string, unknown> = {
      "GET /api/setup/registry": { entries: [{ service: "api", local_path: "/repos/api" }] },
      "POST /api/setup/select": { restarting: true },
      "GET /api/setup/status": { needs_config: false, needs_index: false },
    };
    (globalThis as any).fetch = fakeFetch(routes);
    render(() => <WorkspaceSwitcher />, container);

    await vi.waitFor(() => expect(container.querySelector('[data-testid="workspace-switcher-open"]')).toBeTruthy());
    (container.querySelector('[data-testid="workspace-switcher-open"]') as HTMLElement).click();

    await vi.waitFor(() => expect(setupStore.selecting()).toBe("/repos/api"));
    await vi.waitFor(() => expect(reload).toHaveBeenCalled(), { timeout: 3000 });
  });
});
