import { render } from "solid-js/web";
import { createSignal } from "solid-js";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import SourcePanel from "./SourcePanel";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

// Enough microtask ticks for a fetch promise plus json() plus the effect's
// state update to settle, without pulling in fake timers.
async function flush() {
  for (let i = 0; i < 10; i++) await Promise.resolve();
}

const RANGE_RESP = {
  file: "app/user.rb",
  start: 10,
  end: 15,
  context: 5,
  first_line: 5,
  lines: ["a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"],
};

describe("SourcePanel", () => {
  let container: HTMLElement;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    container.remove();
  });

  it("fetches the bounded range and highlights the node's own extent", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/node/n1/source?range=1&context=5": RANGE_RESP,
    });
    render(() => <SourcePanel nodeId="n1" />, container);
    await flush();

    expect(container.querySelector('[data-testid="source-location"]')?.textContent).toBe("app/user.rb:10–15");
    const lines = container.querySelectorAll('[data-testid="source-line"]');
    expect(lines.length).toBe(11); // first_line 5 .. 15

    const highlighted = Array.from(lines).filter((l) => l.getAttribute("data-highlighted") === "true");
    expect(highlighted.map((l) => l.getAttribute("data-line"))).toEqual(["10", "11", "12", "13", "14", "15"]);
    const dimmed = Array.from(lines).filter((l) => l.getAttribute("data-highlighted") === "false");
    expect(dimmed.map((l) => l.getAttribute("data-line"))).toEqual(["5", "6", "7", "8", "9"]);
  });

  it("falls back to the whole file honestly when end_line is 0", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/node/n2/source?range=1&context=5": {
        file: "app/mystery.rb", start: 3, end: 0, context: 5, first_line: 1, lines: ["x", "y", "z"],
      },
    });
    render(() => <SourcePanel nodeId="n2" />, container);
    await flush();

    expect(container.querySelector('[data-testid="source-location"]')?.textContent).toBe("app/mystery.rb:3");
    expect(container.querySelector('[data-testid="source-expand-context"]')).toBeNull();
    const highlighted = container.querySelectorAll('[data-highlighted="true"]');
    expect(highlighted.length).toBe(0); // no fabricated end — nothing highlighted
  });

  it("expand-context refetches with a widened context param", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/node/n1/source?range=1&context=5": RANGE_RESP,
      "/api/node/n1/source?range=1&context=15": { ...RANGE_RESP, context: 15, first_line: 1 },
    });
    render(() => <SourcePanel nodeId="n1" />, container);
    await flush();

    (container.querySelector('[data-testid="source-expand-context"]') as HTMLButtonElement).click();
    await flush();

    expect((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.some(
      (c) => c[0] === "/api/node/n1/source?range=1&context=15",
    )).toBe(true);
  });

  it("whole-file toggle fetches the unranged endpoint", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/node/n1/source?range=1&context=5": RANGE_RESP,
      "/api/node/n1/source": { source: "line1\nline2\nline3" },
    });
    render(() => <SourcePanel nodeId="n1" />, container);
    await flush();

    (container.querySelector('[data-testid="source-toggle-whole-file"]') as HTMLButtonElement).click();
    await flush();

    const lines = container.querySelectorAll('[data-testid="source-line"]');
    expect(lines.length).toBe(3);
    expect(container.querySelector('[data-testid="source-toggle-whole-file"]')?.textContent).toBe("bounded");
  });

  it("copy path copies the canonical file:start–end format", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/node/n1/source?range=1&context=5": RANGE_RESP,
    });
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    render(() => <SourcePanel nodeId="n1" />, container);
    await flush();

    const copyBtn = Array.from(container.querySelectorAll("button")).find((b) => b.textContent === "copy path");
    copyBtn?.click();

    expect(writeText).toHaveBeenCalledWith("app/user.rb:10–15");
  });

  it("re-fetches and resets context/whole-file state when nodeId changes", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/node/n1/source?range=1&context=5": RANGE_RESP,
      "/api/node/n3/source?range=1&context=5": { ...RANGE_RESP, file: "app/other.rb", start: 1, end: 2, first_line: 1, lines: ["a", "b"] },
    });
    const [nodeId, setNodeId] = createSignal("n1");
    render(() => <SourcePanel nodeId={nodeId()} />, container);
    await flush();
    setNodeId("n3");
    await flush();

    expect(container.querySelector('[data-testid="source-location"]')?.textContent).toBe("app/other.rb:1–2");
  });
});
