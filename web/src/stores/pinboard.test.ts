import { describe, it, expect, beforeEach } from "vitest";
import { pinboardStore } from "./pinboard";
import { scopeStore } from "./scope";

describe("pinboardStore", () => {
  beforeEach(() => scopeStore.reset());

  it("pin adds a chip, ignoring a duplicate id", () => {
    pinboardStore.pin({ id: "a", label: "A" });
    pinboardStore.pin({ id: "a", label: "A (dup)" });
    expect(pinboardStore.pins()).toEqual([{ id: "a", label: "A" }]);
  });

  it("unpin removes a chip", () => {
    pinboardStore.pin({ id: "a", label: "A" });
    pinboardStore.pin({ id: "b", label: "B" });
    pinboardStore.unpin("a");
    expect(pinboardStore.pins()).toEqual([{ id: "b", label: "B" }]);
  });

  it("toggle pins then unpins the same id", () => {
    pinboardStore.toggle({ id: "a", label: "A" });
    expect(pinboardStore.isPinned("a")).toBe(true);
    pinboardStore.toggle({ id: "a", label: "A" });
    expect(pinboardStore.isPinned("a")).toBe(false);
  });

  it("clear empties the tray", () => {
    pinboardStore.pin({ id: "a", label: "A" });
    pinboardStore.pin({ id: "b", label: "B" });
    pinboardStore.clear();
    expect(pinboardStore.pins()).toEqual([]);
  });

  it("pinboard mode (active) only engages at 2+ pins — 1 pin only badges", () => {
    expect(pinboardStore.active()).toBe(false);
    pinboardStore.pin({ id: "a", label: "A" });
    expect(pinboardStore.active()).toBe(false);
    pinboardStore.pin({ id: "b", label: "B" });
    expect(pinboardStore.active()).toBe(true);
  });

  // Fade-not-remove: pin/unpin must never abort the active scope's in-flight
  // fetch (scopeStore.setPins uses plain `commit`, not `commitStackChange`)
  // — unpinning restores instantly with no refetch of the underlying scope.
  it("pin/unpin never aborts or replaces the active scope's fetch signal", () => {
    const before = scopeStore.signal();
    pinboardStore.pin({ id: "a", label: "A" });
    pinboardStore.pin({ id: "b", label: "B" });
    pinboardStore.unpin("a");
    pinboardStore.clear();
    expect(before.aborted).toBe(false);
    expect(scopeStore.signal()).toBe(before);
  });

  it("round-trips pins through the URL codec via scopeStore.setPins", () => {
    pinboardStore.pin({ id: "a", label: "A" });
    pinboardStore.pin({ id: "b", label: "B" });
    expect(scopeStore.viewState().pins).toEqual([
      { id: "a", label: "A" },
      { id: "b", label: "B" },
    ]);
  });
});
