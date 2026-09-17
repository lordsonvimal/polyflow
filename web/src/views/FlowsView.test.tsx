import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import FlowsView from "./FlowsView";
import { runtimeViewStore } from "../stores/runtimeView";
import { captureStore } from "../stores/capture";
import { scopeStore } from "../stores/scope";

describe("FlowsView tab bar", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    (globalThis as any).fetch = vi.fn((url: string) => {
      const body = url.includes("/api/flows/entrypoints")
        ? { entrypoints: [], skipped: [] }
        : { active: [], sessions: [] };
      return Promise.resolve({ ok: true, json: async () => body } as Response);
    });
    runtimeViewStore.setTab("catalog");
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    dispose?.();
    captureStore.stopPolling();
    captureStore.reset();
    runtimeViewStore.setTab("catalog");
    scopeStore.reset();
    container.remove();
    vi.restoreAllMocks();
  });

  it("defaults to Catalog and switches to the Runtime tab on click", async () => {
    dispose = render(() => <FlowsView />, container);

    expect(container.querySelector('[data-testid="flows-tab-catalog"]')?.className).toContain("bg-neutral-800");
    expect(container.querySelector('[data-testid="runtime-tab"]')).toBeFalsy();

    (container.querySelector('[data-testid="flows-tab-runtime"]') as HTMLElement).click();
    await new Promise((r) => setTimeout(r, 0));

    expect(container.querySelector('[data-testid="runtime-tab"]')).toBeTruthy();
  });
});
