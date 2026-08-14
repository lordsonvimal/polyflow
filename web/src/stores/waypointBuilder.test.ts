import { describe, it, expect, beforeEach } from "vitest";
import { waypointBuilderStore } from "./waypointBuilder";

describe("waypointBuilderStore", () => {
  beforeEach(() => {
    waypointBuilderStore.clear();
    waypointBuilderStore.consumeSeed();
    waypointBuilderStore.setDirection("forward");
  });

  it("requestStart seeds a fresh single-chip session and leaves a one-shot seed request", () => {
    expect(waypointBuilderStore.isActive()).toBe(false);
    waypointBuilderStore.requestStart({ id: "seed", label: "Seed" });
    expect(waypointBuilderStore.waypoints()).toEqual([{ id: "seed", label: "Seed" }]);
    expect(waypointBuilderStore.direction()).toBe("forward");
    expect(waypointBuilderStore.requestedSeed()).toEqual({ id: "seed", label: "Seed" });
    waypointBuilderStore.consumeSeed();
    expect(waypointBuilderStore.requestedSeed()).toBeNull();
  });

  it("append adds to the end, prepend adds to the start, both dedupe existing ids", () => {
    waypointBuilderStore.requestStart({ id: "a", label: "A" });
    waypointBuilderStore.append({ id: "b", label: "B" });
    waypointBuilderStore.prepend({ id: "z", label: "Z" });
    expect(waypointBuilderStore.waypoints().map((w) => w.id)).toEqual(["z", "a", "b"]);

    waypointBuilderStore.append({ id: "a", label: "A" });
    waypointBuilderStore.prepend({ id: "b", label: "B" });
    expect(waypointBuilderStore.waypoints().map((w) => w.id)).toEqual(["z", "a", "b"]);
  });

  it("removeAt removes a chip mid-list, keeping the rest for editing", () => {
    waypointBuilderStore.requestStart({ id: "a", label: "A" });
    waypointBuilderStore.append({ id: "b", label: "B" });
    waypointBuilderStore.append({ id: "c", label: "C" });
    waypointBuilderStore.removeAt(1);
    expect(waypointBuilderStore.waypoints().map((w) => w.id)).toEqual(["a", "c"]);
  });

  it("clear empties the session", () => {
    waypointBuilderStore.requestStart({ id: "a", label: "A" });
    waypointBuilderStore.clear();
    expect(waypointBuilderStore.waypoints()).toEqual([]);
    expect(waypointBuilderStore.isActive()).toBe(false);
  });
});
