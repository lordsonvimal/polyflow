import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import DocsPanel from "./DocsPanel";
import { KEY_BINDINGS } from "../../interaction/keys";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const entry = routes[u.pathname];
    if (!entry) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => entry } as Response);
  });
}

const CLI_FIXTURE = {
  commands: [
    {
      name: "index",
      short: "Build the graph",
      usage: "polyflow index [flags]",
      flags: [{ name: "full", usage: "full re-index", default: "false" }],
    },
    {
      name: "config",
      short: "Edit workspace config",
      subcommands: [
        {
          name: "service",
          short: "Manage services",
          subcommands: [{ name: "add", short: "Add a service" }],
        },
      ],
    },
  ],
};

describe("DocsPanel", () => {
  let container: HTMLElement;
  let dispose: () => void;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    (globalThis as any).fetch = fakeFetch({ "/api/docs/cli": CLI_FIXTURE });
    (globalThis.navigator as any).clipboard = { writeText: vi.fn() };
  });

  afterEach(() => {
    dispose?.();
    container.remove();
    vi.restoreAllMocks();
  });

  it("defaults to the Setup section with the MCP snippet and config example", () => {
    dispose = render(() => <DocsPanel />, container);
    expect(container.querySelector('[data-testid="docs-setup"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="docs-mcp-snippet"]')?.textContent).toBe("claude mcp add polyflow -- polyflow mcp");
    expect(container.querySelector('[data-testid="docs-config-example"]')?.textContent).toContain("services:");
  });

  it("switches to CLI reference and renders every fixture command, including nested ones", async () => {
    dispose = render(() => <DocsPanel />, container);
    (container.querySelector('[data-testid="docs-nav-cli"]') as HTMLElement).click();

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="cli-command"]')).toHaveLength(4));

    const text = container.querySelector('[data-testid="docs-cli"]')?.textContent ?? "";
    expect(text).toContain("polyflow index");
    expect(text).toContain("polyflow config service add");
    expect(text).toContain("full re-index");
  });

  it("CLI search filters the anchor list to matching commands", async () => {
    dispose = render(() => <DocsPanel />, container);
    (container.querySelector('[data-testid="docs-nav-cli"]') as HTMLElement).click();
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="docs-cli-anchor-link"]')).toHaveLength(4));

    const search = container.querySelector('[data-testid="docs-cli-search"]') as HTMLInputElement;
    search.value = "add";
    search.dispatchEvent(new Event("input", { bubbles: true }));

    const links = container.querySelectorAll('[data-testid="docs-cli-anchor-link"]');
    expect(links).toHaveLength(1);
    expect(links[0].textContent).toBe("config service add");
    expect(links[0].getAttribute("href")).toBe("#cli-config-service-add");
  });

  it("switches to UI guide and renders every KEY_BINDINGS entry in the shortcut sheet", () => {
    dispose = render(() => <DocsPanel />, container);
    (container.querySelector('[data-testid="docs-nav-guide"]') as HTMLElement).click();

    const rows = container.querySelectorAll('[data-testid="docs-shortcut-row"]');
    expect(rows).toHaveLength(KEY_BINDINGS.length);
    expect(container.querySelector('[data-testid="docs-guide-rendered"]')).toBeTruthy();
  });

  it("switches to Concepts and renders the rendered markdown", () => {
    dispose = render(() => <DocsPanel />, container);
    (container.querySelector('[data-testid="docs-nav-concepts"]') as HTMLElement).click();

    const rendered = container.querySelector('[data-testid="docs-concepts-rendered"]');
    expect(rendered).toBeTruthy();
    expect(rendered?.textContent).toContain("trust contract");
  });
});
