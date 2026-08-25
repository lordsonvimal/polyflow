import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import Breadcrumbs from "./Breadcrumbs";
import { scopeStore } from "../stores/scope";

describe("Breadcrumbs", () => {
  let container: HTMLElement;

  beforeEach(() => {
    scopeStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => container.remove());

  function crumb(label: string): HTMLElement | undefined {
    return [...container.querySelectorAll("button")].find((b) => b.textContent === label);
  }

  it("renders every crumb inline when the stack is shallow", () => {
    scopeStore.push({ kind: "service", service: "svc-a" });
    scopeStore.push({ kind: "folder", service: "svc-a", path: "src" });
    render(() => <Breadcrumbs />, container);

    expect(crumb("overview")).toBeTruthy();
    expect(crumb("svc-a")).toBeTruthy();
    expect(crumb("src")).toBeTruthy();
    expect(crumb("…")).toBeFalsy();
  });

  it("collapses the middle of a deep stack into a single '…' crumb", () => {
    scopeStore.push({ kind: "service", service: "svc-a" });
    scopeStore.push({ kind: "service", service: "svc-b" });
    scopeStore.push({ kind: "service", service: "svc-c" });
    scopeStore.push({ kind: "service", service: "svc-d" });
    scopeStore.push({ kind: "service", service: "svc-e" });
    render(() => <Breadcrumbs />, container);

    // root + collapsed "…" + last two crumbs stay visible
    expect(crumb("overview")).toBeTruthy();
    expect(crumb("svc-d")).toBeTruthy();
    expect(crumb("svc-e")).toBeTruthy();
    expect(crumb("…")).toBeTruthy();
    // everything folded away isn't rendered as its own top-level button
    expect(crumb("svc-a")).toBeFalsy();
    expect(crumb("svc-b")).toBeFalsy();
    expect(crumb("svc-c")).toBeFalsy();
  });

  it("clicking a hidden crumb in the collapsed menu jumps to it", () => {
    scopeStore.push({ kind: "service", service: "svc-a" });
    scopeStore.push({ kind: "service", service: "svc-b" });
    scopeStore.push({ kind: "service", service: "svc-c" });
    scopeStore.push({ kind: "service", service: "svc-d" });
    scopeStore.push({ kind: "service", service: "svc-e" });
    render(() => <Breadcrumbs />, container);

    const toggle = [...container.querySelectorAll("button")].find(
      (b) => b.getAttribute("data-testid") === "breadcrumb-collapsed",
    )!;
    toggle.click();

    // The menu portals to <body> (outside `container`) so it isn't clipped
    // by the breadcrumb strip's overflow-x-auto.
    const menu = document.querySelector('[data-testid="breadcrumb-collapsed-menu"]')!;
    const hidden = [...menu.querySelectorAll("button")].find((b) => b.textContent === "svc-b")!;
    hidden.click();

    expect(scopeStore.stack().map((s) => (s as { service?: string }).service ?? s.kind)).toEqual([
      "overview",
      "svc-a",
      "svc-b",
    ]);
  });
});
