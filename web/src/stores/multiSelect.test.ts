import { describe, it, expect, beforeEach } from "vitest";
import { multiSelectStore } from "./multiSelect";

describe("multiSelectStore", () => {
  beforeEach(() => multiSelectStore.clear());

  it("toggle adds an id not yet present", () => {
    multiSelectStore.toggle("a");
    expect([...multiSelectStore.ids()]).toEqual(["a"]);
  });

  it("toggle removes an id already present", () => {
    multiSelectStore.toggle("a");
    multiSelectStore.toggle("a");
    expect(multiSelectStore.ids().size).toBe(0);
  });

  it("setIds replaces the whole set", () => {
    multiSelectStore.toggle("a");
    multiSelectStore.setIds(new Set(["b", "c"]));
    expect([...multiSelectStore.ids()].sort()).toEqual(["b", "c"]);
  });

  it("clear empties the set", () => {
    multiSelectStore.setIds(new Set(["a", "b"]));
    multiSelectStore.clear();
    expect(multiSelectStore.ids().size).toBe(0);
  });

  it("addAll unions new ids up to the cap", () => {
    multiSelectStore.setIds(new Set(["a"]));
    const { added, capped } = multiSelectStore.addAll(["b", "c"], 10);
    expect(added).toBe(2);
    expect(capped).toBe(false);
    expect([...multiSelectStore.ids()].sort()).toEqual(["a", "b", "c"]);
  });

  it("addAll stops adding once the cap is hit and reports capped", () => {
    multiSelectStore.setIds(new Set(["a", "b"]));
    const { capped } = multiSelectStore.addAll(["c", "d", "e"], 3);
    expect(capped).toBe(true);
    expect(multiSelectStore.ids().size).toBe(3);
  });

  it("addAll is idempotent for ids already selected (no false capping)", () => {
    multiSelectStore.setIds(new Set(["a", "b", "c"]));
    const { added, capped } = multiSelectStore.addAll(["a", "b", "c"], 3);
    expect(added).toBe(0);
    expect(capped).toBe(false);
  });
});
