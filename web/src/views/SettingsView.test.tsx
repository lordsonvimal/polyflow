import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import SettingsView from "./SettingsView";
import { patternsStore } from "../stores/patterns";
import { setupStore } from "../stores/setup";

function fakeFetch() {
  return vi.fn((url: string) => {
    const path = new URL(url, "http://localhost").pathname;
    if (path === "/api/setup/agents") {
      return Promise.resolve({ ok: true, status: 200, json: async () => ({ scope: "repo", agents: [] }) } as Response);
    }
    return Promise.resolve({ ok: true, status: 200, json: async () => ({ patterns: [] }) } as Response);
  });
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
    setupStore.reset();
  });

  it("shows the Patterns panel by default", () => {
    expect(container.querySelector('[data-testid="patterns-panel"]')).toBeTruthy();
  });

  it("shows the Agents panel when the Agents section is selected", async () => {
    (container.querySelector('[data-testid="settings-nav-agents"]') as HTMLElement).click();
    await vi.waitFor(() => expect(container.querySelector('[data-testid="agent-setup-panel"]')).toBeTruthy());
  });
});
