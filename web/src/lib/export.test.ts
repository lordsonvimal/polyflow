import { describe, it, expect, vi, afterEach } from "vitest";
import { mermaidURL, exportFilename, fetchMermaid } from "./export";

afterEach(() => vi.restoreAllMocks());

describe("mermaidURL", () => {
  it("builds a whole-graph export URL", () => {
    expect(mermaidURL("service")).toBe("/api/export/mermaid?level=service");
  });

  it("scopes to the active trace when one is set", () => {
    const url = mermaidURL("function", { root: "svc:f.go:function:X:1", direction: "forward", depth: 5 });
    const sp = new URLSearchParams(url.split("?")[1]);
    expect(sp.get("level")).toBe("function");
    expect(sp.get("root")).toBe("svc:f.go:function:X:1");
    expect(sp.get("direction")).toBe("forward");
    expect(sp.get("depth")).toBe("5");
  });
});

describe("exportFilename", () => {
  it("names files by kind and level with a date stamp", () => {
    expect(exportFilename("mermaid", "service")).toMatch(/^polyflow-service-\d{4}-\d{2}-\d{2}\.mmd$/);
    expect(exportFilename("svg")).toMatch(/\.svg$/);
    expect(exportFilename("png")).toMatch(/\.png$/);
  });
});

describe("fetchMermaid", () => {
  it("returns the server's diagram text", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("flowchart LR\n", { status: 200 })));
    await expect(fetchMermaid("service")).resolves.toBe("flowchart LR\n");
  });

  it("throws on a failed export", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("nope", { status: 400 })));
    await expect(fetchMermaid("service")).rejects.toThrow("export failed: 400");
  });
});
