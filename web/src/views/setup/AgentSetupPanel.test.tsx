import { render } from "solid-js/web";
import { describe, it, expect, afterEach, vi } from "vitest";
import AgentSetupPanel from "./AgentSetupPanel";
import { setupStore } from "../../stores/setup";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string, init?: RequestInit) => {
    const u = new URL(url, "http://localhost");
    const key = `${init?.method ?? "GET"} ${u.pathname}`;
    let match = routes[key] ?? routes[u.pathname];
    if (typeof match === "function") match = (match as () => unknown)();
    if (match === undefined) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, status: 200, json: async () => match, text: async () => JSON.stringify(match) } as Response);
  });
}

const AGENTS_REPO = {
  scope: "repo",
  agents: [
    {
      name: "cursor",
      display_name: "Cursor",
      description: "MCP support via mcp.json — no post-tool-use hook mechanism",
      supports_hooks: false,
      supports_global_scope: false,
      mcp_configured: false,
      hooks_configured: false,
    },
  ],
};

describe("AgentSetupPanel", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  afterEach(() => {
    dispose?.();
    container.remove();
    setupStore.reset();
  });

  function mount(routes: Record<string, unknown>) {
    (globalThis as any).fetch = fakeFetch(routes);
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <AgentSetupPanel />, container);
  }

  it("lists agents with current registration status", async () => {
    mount({ "GET /api/setup/agents": AGENTS_REPO });

    await vi.waitFor(() => expect(container.querySelector('[data-testid="agent-setup-row"]')).toBeTruthy());
    expect(container.textContent).toContain("Cursor");
    expect(container.textContent).toContain("MCP not configured");
  });

  it("configures an agent and reflects the new status without a page reload", async () => {
    const configuredAfter = {
      scope: "repo",
      agents: [{ ...AGENTS_REPO.agents[0], mcp_configured: true }],
    };
    let getCallCount = 0;
    mount({
      "GET /api/setup/agents": () => {
        getCallCount += 1;
        return getCallCount === 1 ? AGENTS_REPO : configuredAfter;
      },
      "POST /api/setup/agent": { mcp_result: "Created .cursor/mcp.json with the polyflow MCP server.", hooks_skipped: "Cursor has no post-tool-use hook mechanism" },
    });

    await vi.waitFor(() => expect(container.querySelector('[data-testid="agent-setup-apply-button"]')).toBeTruthy());
    (container.querySelector('[data-testid="agent-setup-apply-button"]') as HTMLElement).click();

    await vi.waitFor(() => expect(container.querySelector('[data-testid="agent-setup-result"]')).toBeTruthy());
    expect(container.textContent).toContain("Created .cursor/mcp.json");
    await vi.waitFor(() => expect(container.textContent).toContain("MCP configured"));
  });
});
