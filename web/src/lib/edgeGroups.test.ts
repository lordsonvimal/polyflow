import { describe, it, expect } from "vitest";
import { EDGE_GROUPS, EDGE_GROUP_NAMES, edgeGroupOf, edgeTypesForGroups } from "./edgeGroups";

describe("edgeGroups", () => {
  it("partitions types with no duplicates across groups", () => {
    const seen = new Map<string, string>();
    for (const [group, types] of Object.entries(EDGE_GROUPS)) {
      for (const t of types) {
        expect(seen.has(t), `"${t}" appears in both "${seen.get(t)}" and "${group}"`).toBe(false);
        seen.set(t, group);
      }
    }
  });

  it("edgeGroupOf finds a listed type's group", () => {
    expect(edgeGroupOf("calls")).toBe("calls");
    expect(edgeGroupOf("http_call")).toBe("http");
    expect(edgeGroupOf("publishes")).toBe("messaging");
    expect(edgeGroupOf("reads")).toBe("data-flow");
    expect(edgeGroupOf("dom_read")).toBe("dom");
    expect(edgeGroupOf("imports")).toBe("structure");
  });

  it("edgeGroupOf falls back to structure for an unlisted type", () => {
    expect(edgeGroupOf("some_future_edge_type")).toBe("structure");
  });

  it("edgeTypesForGroups flattens the requested groups", () => {
    expect(edgeTypesForGroups(["calls"])).toEqual(EDGE_GROUPS.calls);
    expect(edgeTypesForGroups(["calls", "dom"])).toEqual([...EDGE_GROUPS.calls, ...EDGE_GROUPS.dom]);
    expect(edgeTypesForGroups([])).toEqual([]);
  });

  it("has the six FilterBar-pinned group names", () => {
    expect(EDGE_GROUP_NAMES.sort()).toEqual(
      ["calls", "http", "messaging", "data-flow", "dom", "structure"].sort()
    );
  });
});
