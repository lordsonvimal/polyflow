import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import SettingsView from "./SettingsView";
import { patternsStore } from "../stores/patterns";

function fakeFetch() {
  return vi.fn(() => Promise.resolve({ ok: true, status: 200, json: async () => ({ patterns: [] }) } as Response));
}

describe("SettingsView", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    patternsStore.reset();
    (globalThis as any).fetch = fakeFetch();
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <SettingsView />, container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  it("shows the Patterns panel by default", () => {
    expect(container.querySelector('[data-testid="patterns-panel"]')).toBeTruthy();
  });
});
