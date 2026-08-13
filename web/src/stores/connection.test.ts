import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { connectionStore } from "./connection";

// Minimal fake EventSource so we control open/error deterministically —
// jsdom does not implement EventSource.
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeEventSource.instances.push(this);
  }
  close() {
    this.closed = true;
  }
}

describe("connectionStore", () => {
  const realES = (global as any).EventSource;

  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.instances = [];
    (global as any).EventSource = FakeEventSource;
  });

  afterEach(() => {
    connectionStore.stop();
    vi.useRealTimers();
    (global as any).EventSource = realES;
  });

  it("starts connecting, then connected on open", () => {
    connectionStore.start();
    expect(connectionStore.state()).toBe("connecting");
    FakeEventSource.instances[0].onopen?.();
    expect(connectionStore.state()).toBe("connected");
  });

  it("goes disconnected on error and schedules a reconnect with a countdown", () => {
    connectionStore.start();
    FakeEventSource.instances[0].onopen?.();
    FakeEventSource.instances[0].onerror?.();
    expect(connectionStore.state()).toBe("disconnected");
    expect(connectionStore.retryIn()).toBeGreaterThan(0);
  });

  it("reconnects automatically and clears the banner state", () => {
    connectionStore.start();
    FakeEventSource.instances[0].onerror?.();
    expect(connectionStore.state()).toBe("disconnected");

    vi.advanceTimersByTime(1000);
    expect(FakeEventSource.instances.length).toBe(2);
    FakeEventSource.instances[1].onopen?.();
    expect(connectionStore.state()).toBe("connected");
  });

  it("reconnectNow bypasses the countdown", () => {
    connectionStore.start();
    FakeEventSource.instances[0].onerror?.();
    connectionStore.reconnectNow();
    expect(FakeEventSource.instances.length).toBe(2);
  });

  it("stop closes the connection and prevents further reconnects", () => {
    connectionStore.start();
    const es = FakeEventSource.instances[0];
    connectionStore.stop();
    expect(es.closed).toBe(true);
    es.onerror?.();
    vi.advanceTimersByTime(60_000);
    expect(FakeEventSource.instances.length).toBe(1);
  });
});
