import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import BottomDrawer from "./BottomDrawer";
import { drawerStore } from "../stores/drawer";
import { contextCopyStore } from "../stores/contextCopy";
import { scopeStore } from "../stores/scope";

function fakeFetch(routes: Record<string, unknown | { status: number; body: string }>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const match = Object.keys(routes).find((k) => u.pathname === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    const entry = routes[match];
    if (entry && typeof entry === "object" && "status" in (entry as object) && "body" in (entry as object)) {
      const e = entry as { status: number; body: string };
      return Promise.resolve({ ok: false, status: e.status, text: async () => e.body } as Response);
    }
    return Promise.resolve({ ok: true, json: async () => entry } as Response);
  });
}

describe("BottomDrawer / Context tab", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    scopeStore.reset();
    drawerStore.setOpen(false);
    drawerStore.setActiveTab("context");
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <BottomDrawer />, container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  it("renders a placeholder before anything has been copied", () => {
    drawerStore.setOpen(true);
    expect(container.textContent).toContain("No context copied yet");
  });

  it("resizes by dragging the resize handle", () => {
    drawerStore.setOpen(true);
    drawerStore.setHeight(260);
    const handle = container.querySelector('[data-testid="drawer-resize-handle"]') as HTMLElement;
    const outer = container.querySelector('[data-testid="bottom-drawer"]') as HTMLElement;
    expect(handle).toBeTruthy();

    handle.dispatchEvent(new MouseEvent("mousedown", { clientY: 500, bubbles: true }));
    window.dispatchEvent(new MouseEvent("mousemove", { clientY: 400 })); // dragged up 100px -> grows
    expect(drawerStore.height()).toBe(360);
    expect(outer.style.height).toBe("360px");

    window.dispatchEvent(new MouseEvent("mousemove", { clientY: 550 })); // dragged down past start -> shrinks, clamped
    expect(drawerStore.height()).toBe(210);

    window.dispatchEvent(new MouseEvent("mouseup"));
    window.dispatchEvent(new MouseEvent("mousemove", { clientY: 100 }));
    expect(drawerStore.height()).toBe(210); // no longer tracking after mouseup
  });

  it("renders truncation warnings with the omitted list prominently", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/context/bundle": {
        markdown: "# Context: n1\n\nbody\n",
        tokens_estimate: 9000,
        truncated: true,
        omitted: ["n2", "n3"],
      },
    });
    await contextCopyStore.copy({ kind: "node", id: "n1" });
    const warning = container.querySelector('[data-testid="context-truncated-warning"]') as HTMLElement;
    expect(warning).toBeTruthy();
    expect(warning.textContent).toContain("n2");
    expect(warning.textContent).toContain("n3");
  });

  it("renders the token estimate and switches between rendered and raw preview", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/context/bundle": { markdown: "# Context: n1\n\nbody text\n", tokens_estimate: 42, truncated: false, omitted: [] },
    });
    await contextCopyStore.copy({ kind: "node", id: "n1" });

    expect(container.querySelector('[data-testid="context-token-estimate"]')!.textContent).toContain("42");
    expect(container.querySelector('[data-testid="context-preview-rendered"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="context-preview-raw"]')).toBeFalsy();

    (container.querySelector('[data-testid="context-raw-toggle"]') as HTMLElement).click();
    expect(container.querySelector('[data-testid="context-preview-raw"]')!.textContent).toBe("# Context: n1\n\nbody text\n");
    expect(container.querySelector('[data-testid="context-preview-rendered"]')).toBeFalsy();
  });

  it("Copy calls the clipboard with the exact markdown", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/context/bundle": { markdown: "exact markdown\n", tokens_estimate: 3, truncated: false, omitted: [] },
    });
    await contextCopyStore.copy({ kind: "node", id: "n1" });

    const writeText = vi.fn(() => Promise.resolve());
    Object.assign(navigator, { clipboard: { writeText } });
    (container.querySelector('[data-testid="context-copy-clipboard"]') as HTMLElement).click();
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith("exact markdown\n"));
  });

  it("shows an error with a refresh-view action, verbatim from the server", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/context/bundle": { status: 404, body: JSON.stringify({ error: "unknown id(s): gone" }) },
    });
    await contextCopyStore.copy({ kind: "node", id: "gone" });
    const err = container.querySelector('[data-testid="context-error"]') as HTMLElement;
    expect(err.textContent).toContain("unknown id(s): gone");
    expect(container.querySelector('[data-testid="context-refresh-view"]')).toBeTruthy();
  });

  it("lists recent bundles (most-recent first) and re-copy re-opens without refetching", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/context/bundle": { markdown: "md\n", tokens_estimate: 1, truncated: false, omitted: [] },
    });
    await contextCopyStore.copy({ kind: "node", id: "n1" });
    await contextCopyStore.copy({ kind: "node", id: "n2" });

    const items = container.querySelectorAll('[data-testid="context-recent-item"]');
    expect((items[0] as HTMLElement).textContent).toBe("node n2");
    expect((items[1] as HTMLElement).textContent).toBe("node n1");
  });
});

// UF.6: Unresolved tab — kind filters + free-text search mirroring
// GET /api/unresolved's own params, seeded from a badge's pre-filter.
describe("BottomDrawer / Unresolved tab", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    scopeStore.reset();
    drawerStore.setOpen(false);
    drawerStore.setActiveTab("context");
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <BottomDrawer />, container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  it("opening via a badge pre-filters service + file and fetches /api/unresolved with those params", async () => {
    const calls: string[] = [];
    (globalThis as any).fetch = vi.fn((url: string) => {
      calls.push(url);
      return Promise.resolve({ ok: true, json: async () => ({ refs: [], total: 0 }) } as Response);
    });

    drawerStore.openUnresolvedFor("auth", "auth/user.go");

    await vi.waitFor(() => expect(calls.length).toBeGreaterThan(0));
    const u = new URL(calls[calls.length - 1], "http://localhost");
    expect(u.searchParams.get("service")).toBe("auth");
    expect(u.searchParams.get("q")).toBe("auth/user.go");

    expect(container.querySelector('[data-testid="unresolved-filter-chip"]')!.textContent).toContain("auth");
  });

  it("renders the ref list and re-fetches on kind/search change", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/unresolved": { refs: [{ service: "auth", file: "auth/a.go", line: 3, name: "helper", kind: "call" }], total: 1 },
    });
    drawerStore.openUnresolvedFor("auth", "");
    await vi.waitFor(() => expect(container.querySelector('[data-testid="unresolved-row"]')).toBeTruthy());
    expect(container.querySelector('[data-testid="unresolved-list"]')!.textContent).toContain("helper");

    const calls: string[] = [];
    (globalThis as any).fetch = vi.fn((url: string) => {
      calls.push(url);
      return Promise.resolve({ ok: true, json: async () => ({ refs: [], total: 0 }) } as Response);
    });
    const kindSelect = container.querySelector('[data-testid="unresolved-kind"]') as HTMLSelectElement;
    kindSelect.value = "call";
    kindSelect.dispatchEvent(new Event("change", { bubbles: true }));
    await vi.waitFor(() => expect(calls.length).toBeGreaterThan(0));
    const u = new URL(calls[calls.length - 1], "http://localhost");
    expect(u.searchParams.get("kind")).toBe("call");
  });

  it("clicking a ref pushes its file scope", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/unresolved": { refs: [{ service: "auth", file: "auth/a.go", line: 3, name: "helper", kind: "call" }], total: 1 },
    });
    drawerStore.openUnresolvedFor("auth", "");
    await vi.waitFor(() => expect(container.querySelector('[data-testid="unresolved-row"]')).toBeTruthy());
    (container.querySelector('[data-testid="unresolved-row"]') as HTMLElement).click();
    expect(scopeStore.stack().at(-1)).toEqual({ kind: "file", service: "auth", path: "auth/a.go" });
  });

  // DS.3 follow-up: dom_class_high_fanout refs carry a `targets` list of what
  // the fan-out cap dropped; the drawer must let a user expand it without
  // triggering the row's own click-to-open-file behavior.
  it("expands a suppressed ref's dropped targets and shows why, without opening the file", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/unresolved": {
        refs: [
          {
            service: "app",
            file: "assets/js/list.js",
            line: 1,
            name: ".item",
            kind: "dom_class_high_fanout",
            targets: "views/list.html:10\nviews/list.html:20\n+3 more",
          },
        ],
        total: 1,
      },
    });
    drawerStore.openUnresolvedFor("app", "");
    await vi.waitFor(() => expect(container.querySelector('[data-testid="unresolved-row"]')).toBeTruthy());

    expect(container.querySelector('[data-testid="unresolved-targets"]')).toBeFalsy();

    const stackBefore = scopeStore.stack();
    (container.querySelector('[data-testid="unresolved-expand-toggle"]') as HTMLElement).click();
    expect(scopeStore.stack()).toEqual(stackBefore); // toggle must not also open the file

    const targetsPanel = container.querySelector('[data-testid="unresolved-targets"]') as HTMLElement;
    expect(targetsPanel).toBeTruthy();
    expect(targetsPanel.textContent).toContain("fan-out cap");
    const rows = container.querySelectorAll('[data-testid="unresolved-target-row"]');
    expect(rows).toHaveLength(3);
    expect(rows[0].textContent).toBe("views/list.html:10");
    expect(rows[2].textContent).toBe("+3 more");

    (container.querySelector('[data-testid="unresolved-expand-toggle"]') as HTMLElement).click();
    expect(container.querySelector('[data-testid="unresolved-targets"]')).toBeFalsy();
  });
});
