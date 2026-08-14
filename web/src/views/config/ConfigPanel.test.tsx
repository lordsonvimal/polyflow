import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import ConfigPanel from "./ConfigPanel";
import { configStore } from "../../stores/config";
import { notificationsStore } from "../../stores/notifications";

const RAW = `name: fleet
version: "1"
services:
  - name: auth
    path: ./auth
    language: go
links: []
index:
  exclude:
    - "**/node_modules/**"
settings:
  snippet_lines: 30
  default_layout: dagre-lr
  default_depth: 5
  port: 9400
evidence:
  contract_globs: []
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

describe("ConfigPanel", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    configStore.reset();
    notificationsStore.clear();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
    configStore.reset();
    vi.restoreAllMocks();
  });

  it("loads config on mount and renders the path and a service row", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" } });
    dispose = render(() => <ConfigPanel />, container);

    await vi.waitFor(() => expect(container.querySelector('[data-testid="config-path"]')?.textContent).toBe("/ws/polyflow.yml"));
    expect(container.querySelectorAll('[data-testid="config-service-row"]')).toHaveLength(1);
    expect((container.querySelector('[data-testid="config-service-name-0"]') as HTMLInputElement).value).toBe("auth");
  });

  it("adding an exclude glob in Form mode, then saving, PUTs raw text containing it", async () => {
    const fetchMock = fakeFetch({
      "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" },
      "PUT /api/config": { etag: "e2", ok: true },
    });
    (globalThis as any).fetch = fetchMock;
    dispose = render(() => <ConfigPanel />, container);
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="config-exclude-row"]')).toHaveLength(1));

    (container.querySelector('[data-testid="config-add-exclude"]') as HTMLElement).click();
    const newInput = container.querySelectorAll('[data-testid^="config-exclude-"]')[1] as HTMLInputElement;
    newInput.value = "**/dist/**";
    newInput.dispatchEvent(new Event("input", { bubbles: true }));

    const saveBtn = container.querySelector('[data-testid="config-save"]') as HTMLButtonElement;
    expect(saveBtn.disabled).toBe(false);
    saveBtn.click();

    await vi.waitFor(() => {
      const putCall = fetchMock.mock.calls.find(([, init]: [string, RequestInit]) => init?.method === "PUT");
      expect(putCall).toBeTruthy();
    });
    const putCall = fetchMock.mock.calls.find(([, init]: [string, RequestInit]) => init?.method === "PUT")!;
    const body = JSON.parse((putCall[1] as RequestInit).body as string);
    expect(body.raw).toContain("dist");
  });

  it("switches to YAML mode and edits are reflected in the textarea", async () => {
    (globalThis as any).fetch = fakeFetch({ "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" } });
    dispose = render(() => <ConfigPanel />, container);
    await vi.waitFor(() => expect(container.querySelector('[data-testid="config-path"]')?.textContent).toBe("/ws/polyflow.yml"));

    (container.querySelector('[data-testid="config-mode-yaml"]') as HTMLElement).click();
    const textarea = container.querySelector('[data-testid="config-yaml-textarea"]') as HTMLTextAreaElement;
    expect(textarea.value).toContain("services:");
  });

  it("422 save error renders inline with its mapped section", async () => {
    const fetchMock = fakeFetch({
      "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" },
      "PUT /api/config": { status: 422, body: 'service auth: path "./missing" does not exist or is not a directory' },
    });
    (globalThis as any).fetch = fetchMock;
    dispose = render(() => <ConfigPanel />, container);
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="config-service-row"]')).toHaveLength(1));

    const nameInput = container.querySelector('[data-testid="config-service-name-0"]') as HTMLInputElement;
    nameInput.value = "auth2";
    nameInput.dispatchEvent(new Event("input", { bubbles: true }));
    (container.querySelector('[data-testid="config-save"]') as HTMLElement).click();

    await vi.waitFor(() => expect(container.querySelector('[data-testid="config-save-error"]')).toBeTruthy());
    expect(container.querySelector('[data-testid="config-save-error"]')!.textContent).toContain("services");
    expect(container.querySelector('[data-testid="config-save-error"]')!.textContent).toContain("does not exist");
  });

  it("409 conflict renders keep-mine/take-disk/cancel choices", async () => {
    const fetchMock = fakeFetch({
      "GET /api/config": { path: "/ws/polyflow.yml", raw: RAW, parsed: null, etag: "e1" },
      "PUT /api/config": { status: 409, body: JSON.stringify({ error: "config changed on disk", current_etag: "e9" }) },
    });
    (globalThis as any).fetch = fetchMock;
    dispose = render(() => <ConfigPanel />, container);
    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="config-service-row"]')).toHaveLength(1));

    const nameInput = container.querySelector('[data-testid="config-service-name-0"]') as HTMLInputElement;
    nameInput.value = "auth2";
    nameInput.dispatchEvent(new Event("input", { bubbles: true }));
    (container.querySelector('[data-testid="config-save"]') as HTMLElement).click();

    await vi.waitFor(() => expect(container.querySelector('[data-testid="config-conflict-banner"]')).toBeTruthy());
    expect(container.querySelector('[data-testid="config-conflict-keep-mine"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="config-conflict-take-disk"]')).toBeTruthy();
  });
});
