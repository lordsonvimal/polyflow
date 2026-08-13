import { describe, it, expect, beforeEach } from "vitest";
import { paletteStore } from "./palette";

describe("paletteStore", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("open/close/toggle flip isOpen", () => {
    expect(paletteStore.isOpen()).toBe(false);
    paletteStore.open();
    expect(paletteStore.isOpen()).toBe(true);
    paletteStore.close();
    expect(paletteStore.isOpen()).toBe(false);
    paletteStore.toggle();
    expect(paletteStore.isOpen()).toBe(true);
    paletteStore.toggle();
    expect(paletteStore.isOpen()).toBe(false);
  });

  it("addRecent dedupes by id+kind, most-recent first, persisted to localStorage", () => {
    paletteStore.addRecent({ id: "n1", kind: "symbol", label: "SyncJob.perform" });
    paletteStore.addRecent({ id: "f1", kind: "file", label: "sync.rb" });
    paletteStore.addRecent({ id: "n1", kind: "symbol", label: "SyncJob.perform" });
    expect(paletteStore.recent().map(r => r.id)).toEqual(["n1", "f1"]);
    expect(JSON.parse(localStorage.getItem("pf:paletteRecent")!).length).toBe(2);
  });

  it("caps recent items at 20", () => {
    for (let i = 0; i < 25; i++) {
      paletteStore.addRecent({ id: `n${i}`, kind: "symbol", label: `n${i}` });
    }
    expect(paletteStore.recent().length).toBe(20);
    expect(paletteStore.recent()[0].id).toBe("n24");
  });
});
