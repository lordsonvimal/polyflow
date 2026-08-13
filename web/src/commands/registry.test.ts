import { describe, it, expect } from "vitest";
import { commands, registerCommand } from "./registry";
import { layoutPrefs } from "../stores/layoutPrefs";

describe("command registry", () => {
  it("seeds activity-switch, theme, and copy-link commands", () => {
    const ids = commands().map(c => c.id);
    expect(ids).toContain("activity:flows");
    expect(ids).toContain("theme:toggle");
    expect(ids).toContain("share:copy-link");
  });

  it("running an activity command switches the activity", () => {
    layoutPrefs.setActivity("explore");
    commands().find(c => c.id === "activity:health")!.run();
    expect(layoutPrefs.activity()).toBe("health");
  });

  it("plans 11-13 can contribute new commands, replacing by id", () => {
    const before = commands().length;
    registerCommand({ id: "test:one", label: "Test one", run: () => {} });
    expect(commands().length).toBe(before + 1);
    registerCommand({ id: "test:one", label: "Test one renamed", run: () => {} });
    expect(commands().length).toBe(before + 1);
    expect(commands().find(c => c.id === "test:one")!.label).toBe("Test one renamed");
  });
});
