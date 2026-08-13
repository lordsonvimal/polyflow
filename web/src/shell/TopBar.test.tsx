import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import TopBar from "./TopBar";
import { layoutPrefs } from "../stores/layoutPrefs";

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
