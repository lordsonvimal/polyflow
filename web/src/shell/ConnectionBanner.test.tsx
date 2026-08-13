import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import ConnectionBanner from "./ConnectionBanner";
import { connectionStore } from "../stores/connection";

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) {
    FakeEventSource.instances.push(this);
  }
  close() {}
}

describe("ConnectionBanner", () => {
  let container: HTMLElement;
  const realES = (global as any).EventSource;

  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.instances = [];
    (global as any).EventSource = FakeEventSource;
    container = document.createElement("div");
    document.body.appendChild(container);
  });
  afterEach(() => {
    connectionStore.stop();
    container.remove();
    vi.useRealTimers();
    (global as any).EventSource = realES;
  });

  it("is hidden while connected, appears on disconnect, clears on reconnect", () => {
    render(() => <ConnectionBanner />, container);
    expect(container.querySelector('[data-testid="connection-banner"]')).toBeNull();

    connectionStore.start();
    FakeEventSource.instances[0].onopen?.();
    expect(container.querySelector('[data-testid="connection-banner"]')).toBeNull();

    FakeEventSource.instances[0].onerror?.();
    const banner = container.querySelector('[data-testid="connection-banner"]');
    expect(banner).not.toBeNull();
    expect(banner!.textContent).toContain("Lost connection");

    vi.advanceTimersByTime(1000);
    FakeEventSource.instances[1].onopen?.();
    expect(container.querySelector('[data-testid="connection-banner"]')).toBeNull();
  });
});
