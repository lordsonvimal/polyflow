import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { configStore } from "./config";
import { notificationsStore } from "./notifications";
import { jobsStore } from "./jobs";

const RAW = `name: fleet
version: "1"
services:
  - name: auth
    path: ./auth
    language: go
index:
  exclude:
    - "**/node_modules/**"
settings:
  snippet_lines: 30
  default_layout: dagre-lr
  default_depth: 5
  port: 9400
`;

function fakeFetch(routes: Record<string, unknown | { status: number; body: string }>) {
  return vi.fn((url: string, init?: RequestInit) => {
    const u = new URL(url, "http://localhost");
    const key = `${init?.method ?? "GET"} ${u.pathname}`;
    const match = routes[key] ?? routes[u.pathname];
    if (match === undefined) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    if (match && typeof match === "object" && "status" in (match as object) && "body" in (match as object)) {
      const e = match as { status: number; body: string };
      return Promise.resolve({ ok: false, status: e.status, text: async () => e.body, json: async () => JSON.parse(e.body) } as Response);
    }
    return Promise.resolve({ ok: true, status: 200, json: async () => match, text: async () => JSON.stringify(match) } as Response);
  });
}

describe("configStore", () => {
  beforeEach(() => {
    configStore.reset();
    notificationsStore.clear();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("load fetches GET /api/config and parses the raw text into a form model", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" } });
    await configStore.load();
    expect(configStore.path()).toBe("/ws/polyflow.yml");
    expect(configStore.etag()).toBe("e1");
    expect(configStore.model().services[0].name).toBe("auth");
    expect(configStore.dirty()).toBe(false);
  });

  it("add/remove service row edits land in the PUT body raw text", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" },
      "PUT /api/config": { etag: "e2", ok: true },
    });
    await configStore.load();
    configStore.addRow(["services"], { name: "gateway", path: "./gateway", language: "go" });
    expect(configStore.dirty()).toBe(true);

    await configStore.save();

    const fetchMock = (globalThis as any).fetch as ReturnType<typeof vi.fn>;
    const putCall = fetchMock.mock.calls.find(([, init]: [string, RequestInit]) => init?.method === "PUT");
    expect(putCall).toBeTruthy();
    const body = JSON.parse((putCall![1] as RequestInit).body as string);
    expect(body.etag).toBe("e1");
    expect(body.raw).toContain("gateway");
    expect(configStore.dirty()).toBe(false);
    expect(configStore.etag()).toBe("e2");
  });

  it("removing an exclude glob removes it from the saved raw text", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" },
      "PUT /api/config": { etag: "e2", ok: true },
    });
    await configStore.load();
    configStore.removeRow(["index", "exclude"], 0);
    await configStore.save();

    const fetchMock = (globalThis as any).fetch as ReturnType<typeof vi.fn>;
    const putCall = fetchMock.mock.calls.find(([, init]: [string, RequestInit]) => init?.method === "PUT");
    const body = JSON.parse((putCall![1] as RequestInit).body as string);
    expect(body.raw).not.toContain("node_modules");
  });

  it("save success toasts and wires a 'Re-index now?' action to jobsStore.startIndex", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" },
      "PUT /api/config": { etag: "e2", ok: true },
    });
    const startIndexSpy = vi.spyOn(jobsStore, "startIndex").mockResolvedValue(undefined);
    await configStore.load();
    configStore.setField(["settings", "port"], 9500);
    await configStore.save();

    const toast = notificationsStore.toasts().find((t) => t.kind === "success" && t.message === "Config saved");
    expect(toast).toBeTruthy();
    expect(toast!.action?.label).toBe("Re-index now?");
    toast!.action!.onClick();
    expect(startIndexSpy).toHaveBeenCalledWith(false);
  });

  it("409 on save surfaces a conflict; 'take disk' reloads from server", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" },
      "PUT /api/config": { status: 409, body: JSON.stringify({ error: "config changed on disk", current_etag: "e9" }) },
    });
    await configStore.load();
    configStore.setField(["settings", "port"], 9500);
    await configStore.save();

    expect(configStore.conflict()?.currentEtag).toBe("e9");

    (globalThis as any).fetch = fakeFetch({
      "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW.replace("port: 9400", "port: 9999"), parsed: null, etag: "e9" },
    });
    await configStore.takeDisk();
    expect(configStore.conflict()).toBeNull();
    expect(configStore.etag()).toBe("e9");
    expect(configStore.dirty()).toBe(false);
  });

  it("409 'keep mine' adopts the disk etag and retries the write", async () => {
    let putCount = 0;
    (globalThis as any).fetch = vi.fn((url: string, init?: RequestInit) => {
      const u = new URL(url, "http://localhost");
      const method = init?.method ?? "GET";
      if (method === "GET") {
        return Promise.resolve({ ok: true, json: async () => ({ path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" }) } as Response);
      }
      putCount += 1;
      if (putCount === 1) {
        return Promise.resolve({
          ok: false,
          status: 409,
          text: async () => JSON.stringify({ error: "config changed on disk", current_etag: "e9" }),
        } as Response);
      }
      return Promise.resolve({ ok: true, json: async () => ({ etag: "e10", ok: true }) } as Response);
    });
    await configStore.load();
    configStore.setField(["settings", "port"], 9500);
    await configStore.save();
    expect(configStore.conflict()?.currentEtag).toBe("e9");

    await configStore.keepMine();
    expect(configStore.conflict()).toBeNull();
    expect(configStore.etag()).toBe("e10");
    expect(putCount).toBe(2);
  });

  it("422 on save surfaces the verbatim error mapped to the offending section", async () => {
    (globalThis as any).fetch = fakeFetch({
      "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" },
      "PUT /api/config": { status: 422, body: 'service auth: path "./auth" (resolved to "/ws/auth") does not exist or is not a directory' },
    });
    await configStore.load();
    configStore.setField(["services", 0, "path"], "./missing");
    await configStore.save();

    const err = configStore.saveError();
    expect(err).toBeTruthy();
    expect(err!.section).toBe("services");
    expect(err!.message).toContain("does not exist");
  });

  it("setMode('yaml') then back to 'form' round-trips edits made in YAML mode", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" } });
    await configStore.load();
    configStore.setMode("yaml");
    configStore.editYamlText(configStore.yamlText().replace("auth", "auth-2"));
    configStore.setMode("form");
    expect(configStore.model().services[0].name).toBe("auth-2");
  });

  it("switching from YAML to form mode with invalid YAML blocks the switch and surfaces a parse error", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" } });
    await configStore.load();
    configStore.setMode("yaml");
    configStore.editYamlText("services: [\n  broken");
    configStore.setMode("form");
    expect(configStore.mode()).toBe("yaml");
    expect(configStore.parseError()).toBeTruthy();
  });
});
