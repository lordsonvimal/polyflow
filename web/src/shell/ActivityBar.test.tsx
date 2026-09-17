import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import App from "../App";
import { layoutPrefs } from "../stores/layoutPrefs";

describe("ActivityBar", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;
  beforeEach(() => {
    // Switching to the flows activity mounts Catalog, which fetches
    // /api/flows/entrypoints — without a mock that's a real fetch of a
    // relative URL, which jsdom can't resolve and rejects asynchronously
    // after this test has already finished.
    (globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: true, json: async () => ({ entrypoints: [], skipped: [] }) } as Response));
    layoutPrefs.setActivity("explore");
    container = document.createElement("div");
    document.body.appendChild(container);
  });
  afterEach(() => {
    dispose?.();
    container.remove();
    vi.restoreAllMocks();
  });

  it("switching activity changes panel content but canvas-host is the same DOM node", () => {
    dispose = render(() => <App />, container);

    const canvasBefore = container.querySelector('[data-testid="canvas-host"]') as HTMLElement;
    expect(canvasBefore).not.toBeNull();

    // Switch to flows activity
    const buttons = container.querySelectorAll('[data-testid="activity-bar"] button');
    (buttons[1] as HTMLButtonElement).click(); // flows

    const canvasAfter = container.querySelector('[data-testid="canvas-host"]') as HTMLElement;
    expect(canvasAfter).toBe(canvasBefore); // same DOM node
    expect(container.querySelector('[data-testid="panel-host"]')).not.toBeNull();
  });
});
