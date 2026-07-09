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

import { safeExportScale, MAX_EXPORT_DIM } from "./export";

describe("safeExportScale", () => {
  it("keeps the desired scale for small graphs", () => {
    expect(safeExportScale(1000, 800)).toBe(2);
  });

  it("clamps by max dimension for wide graphs", () => {
    const scale = safeExportScale(20000, 1000);
    expect(scale * 20000).toBeLessThanOrEqual(MAX_EXPORT_DIM);
    expect(scale).toBeGreaterThan(0);
  });

  it("clamps by area for large square graphs", () => {
    const scale = safeExportScale(7000, 7000);
    expect(scale * 7000 * scale * 7000).toBeLessThanOrEqual(32_000_000 + 1);
  });

  it("never returns zero or negative", () => {
    expect(safeExportScale(1e9, 1e9)).toBeGreaterThan(0);
    expect(safeExportScale(0, 0)).toBe(2);
  });
});
