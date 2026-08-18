import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import PatternsPanel from "./PatternsPanel";
import { patternsStore } from "../../stores/patterns";

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

const PATTERNS = {
  patterns: [
    { name: "http_handler_go", language: "go", node_type: "http_handler", roles: ["method", "path"], source: "embedded:go/http.yaml", custom: false },
    { name: "custom_amqp", language: "ruby", node_type: "channel", source: "/ws/.polyflow/patterns/custom.yaml", custom: true },
  ],
};

describe("PatternsPanel", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    patternsStore.reset();
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  function mount(routes: Record<string, unknown>) {
    (globalThis as any).fetch = fakeFetch(routes);
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <PatternsPanel />, container);
  }

  it("loads and lists patterns, marking custom ones", async () => {
    mount({ "GET /api/patterns": PATTERNS });

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="pattern-row"]').length).toBe(2));
    expect(container.textContent).toContain("http_handler_go");
    expect(container.textContent).toContain("custom_amqp");
    expect(container.querySelectorAll('[data-testid="pattern-custom-badge"]').length).toBe(1);
  });

  it("filters by search text", async () => {
    mount({ "GET /api/patterns": PATTERNS });
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="pattern-row"]').length).toBe(2));

    const search = container.querySelector('[data-testid="patterns-search"]') as HTMLInputElement;
    search.value = "amqp";
    search.dispatchEvent(new Event("input", { bubbles: true }));

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="pattern-row"]').length).toBe(1));
    expect(container.textContent).toContain("custom_amqp");
  });

  it("submits the add-pattern form and shows a verbatim 422 error", async () => {
    mount({
      "GET /api/patterns": PATTERNS,
      "POST /api/patterns": { status: 422, body: JSON.stringify({ error: "invalid pattern file: yaml: line 2: bad indent" }) },
    });
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="pattern-row"]').length).toBe(2));

    (container.querySelector('[data-testid="patterns-add-toggle"]') as HTMLElement).click();
    const name = container.querySelector('[data-testid="pattern-add-name"]') as HTMLInputElement;
    name.value = "broken";
    name.dispatchEvent(new Event("input", { bubbles: true }));

    (container.querySelector('[data-testid="pattern-add-form"]') as HTMLFormElement).requestSubmit();

    await vi.waitFor(() => {
      const el = container.querySelector('[data-testid="pattern-add-error"]');
      expect(el).toBeTruthy();
      expect(el!.textContent).toContain("invalid pattern file");
    });
  });
});
