import { describe, it, expect, beforeEach } from "vitest";
import { stackKey, saveViewport, getViewport, resetViewportCache } from "./viewportCache";

describe("viewportCache", () => {
  beforeEach(() => resetViewportCache());

  it("round-trips a saved viewport by stack key", () => {
    const key = stackKey([{ kind: "overview" }, { kind: "service", service: "auth" }]);
    expect(getViewport(key)).toBeUndefined();
    saveViewport(key, { pan: { x: 10, y: 20 }, zoom: 1.5 });
    expect(getViewport(key)).toEqual({ pan: { x: 10, y: 20 }, zoom: 1.5 });
  });

  it("keys are distinct per full stack, not just the top scope", () => {
    const keyA = stackKey([{ kind: "overview" }, { kind: "service", service: "auth" }]);
    const keyB = stackKey([{ kind: "overview" }, { kind: "service", service: "billing" }, { kind: "service", service: "auth" }]);
    expect(keyA).not.toBe(keyB);
  });
});
