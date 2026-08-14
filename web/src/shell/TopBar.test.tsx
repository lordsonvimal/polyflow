import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import TopBar from "./TopBar";
import { layoutPrefs } from "../stores/layoutPrefs";
import { pinboardStore } from "../stores/pinboard";
import { scopeStore } from "../stores/scope";
import { selectionStore } from "../stores/selection";

describe("TopBar theme toggle", () => {
  let container: HTMLElement;
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.classList.remove("dark");
    container = document.createElement("div");
    document.body.appendChild(container);
  });
  afterEach(() => { container.remove(); });

  it("toggle adds dark class and persists", () => {
    layoutPrefs.setTheme("light");
    render(() => <TopBar />, container);

    // find the theme toggle button (☾ or ☀)
    const btns = Array.from(container.querySelectorAll("button")) as HTMLButtonElement[];
    const toggleBtn = btns.find(b => b.textContent === "☾" || b.textContent === "☀")!;
    expect(toggleBtn).not.toBeUndefined();

    toggleBtn.click();
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(localStorage.getItem("pf:theme")).toBe("dark");
  });

  it("toggle removes dark class when already dark", () => {
    layoutPrefs.setTheme("dark");
    document.documentElement.classList.add("dark");
    render(() => <TopBar />, container);

    const btns = Array.from(container.querySelectorAll("button")) as HTMLButtonElement[];
    const toggleBtn = btns.find(b => b.textContent === "☾" || b.textContent === "☀")!;
    toggleBtn.click();
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(localStorage.getItem("pf:theme")).toBe("light");
  });
});

// UF.7: pin tray
describe("TopBar pin tray", () => {
  let container: HTMLElement;
  beforeEach(() => {
    scopeStore.reset();
    selectionStore.setSelection(null);
    container = document.createElement("div");
    document.body.appendChild(container);
  });
  afterEach(() => {
    container.remove();
    scopeStore.reset();
  });

  it("is hidden with no pins, appears with one, badges without 'view as flow lane'", () => {
    render(() => <TopBar />, container);
    expect(container.querySelector('[data-testid="pin-tray"]')).toBeFalsy();

    pinboardStore.pin({ id: "a", label: "Publisher" });
    expect(container.querySelector('[data-testid="pin-tray"]')).toBeTruthy();
    expect(container.querySelectorAll('[data-testid="pin-chip"]')).toHaveLength(1);
    expect(container.querySelector('[data-testid="pin-tray-view-as-lane"]')).toBeFalsy();
  });

  it("shows 'View as flow lane' once 2+ pins are set, and pushes a pins flow scope", () => {
    pinboardStore.pin({ id: "a", label: "Publisher" });
    pinboardStore.pin({ id: "b", label: "Consumer" });
    render(() => <TopBar />, container);

    const btn = container.querySelector('[data-testid="pin-tray-view-as-lane"]') as HTMLElement;
    expect(btn).toBeTruthy();
    btn.click();
    expect(scopeStore.stack().at(-1)).toEqual({ kind: "flow", flow: { kind: "pins", ids: ["a", "b"] } });
  });

  it("chip × unpins; [clear all] empties the tray", () => {
    pinboardStore.pin({ id: "a", label: "Publisher" });
    pinboardStore.pin({ id: "b", label: "Consumer" });
    render(() => <TopBar />, container);

    const chips = () => container.querySelectorAll('[data-testid="pin-chip"]');
    expect(chips()).toHaveLength(2);
    (chips()[0].querySelector("button:last-child") as HTMLElement).click();
    expect(pinboardStore.pins().map((p) => p.id)).toEqual(["b"]);

    (container.querySelector('[data-testid="pin-tray-clear"]') as HTMLElement).click();
    expect(pinboardStore.pins()).toEqual([]);
  });

  it("clicking a chip's label selects that node", () => {
    pinboardStore.pin({ id: "a", label: "Publisher" });
    render(() => <TopBar />, container);

    const label = container.querySelector('[data-testid="pin-chip"] button') as HTMLElement;
    label.click();
    expect(selectionStore.selection()).toEqual({ kind: "node", id: "a" });
  });
});
