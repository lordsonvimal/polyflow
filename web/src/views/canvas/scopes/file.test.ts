import { describe, it, expect, vi } from "vitest";
import { resolveFile } from "./file";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

const SCOPE_RESULT = {
  kind: "file",
  file: "auth/user.go",
  service: "auth",
  nodes: [
    { data: { id: "n1", label: "createUser", type: "function", service: "auth", file: "auth/user.go", line: 10, language: "go" } },
    { data: { id: "n2", label: "validateUser", type: "function", service: "auth", file: "auth/user.go", line: 20, language: "go" } },
    { data: { id: "n3", label: "hashPassword", type: "function", service: "auth", file: "auth/crypto.go", line: 5, language: "go", meta: { stub: "true" } } },
  ],
  edges: [
    { data: { id: "e1", source: "n1", target: "n2", type: "calls" } },
    { data: { id: "e2", source: "n1", target: "n3", type: "calls" } },
  ],
};

describe("resolveFile", () => {
  it("returns the file's symbols plus a boundary stub for the external target", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/scope?kind=file&path=auth%2Fuser.go&service=auth": SCOPE_RESULT });
    const d = await resolveFile({ kind: "file", service: "auth", path: "auth/user.go" });
    expect(d.nodes.map((x) => x.id)).toEqual(["n1", "n2", "n3"]);
    const stub = d.nodes.find((x) => x.id === "n3")!;
    expect(stub.meta).toMatchObject({ stub: "true", stub_kind: "file", stub_service: "auth", stub_path: "auth/crypto.go" });
  });

  it("does not tag in-file nodes as stubs (negative)", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/scope?kind=file&path=auth%2Fuser.go&service=auth": SCOPE_RESULT });
    const d = await resolveFile({ kind: "file", service: "auth", path: "auth/user.go" });
    expect(d.nodes.find((x) => x.id === "n1")?.meta?.stub).toBeUndefined();
  });

  it("is deterministic across two runs", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/scope?kind=file&path=auth%2Fuser.go&service=auth": SCOPE_RESULT });
    const a = await resolveFile({ kind: "file", service: "auth", path: "auth/user.go" });
    const b = await resolveFile({ kind: "file", service: "auth", path: "auth/user.go" });
    expect(a).toEqual(b);
  });
});
