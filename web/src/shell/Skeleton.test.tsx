import { render } from "solid-js/web";
import { describe, it, expect, afterEach } from "vitest";
import { PanelSkeleton, ListSkeleton, TreeSkeleton } from "./Skeleton";

describe("Skeleton components", () => {
  let container: HTMLElement;
  afterEach(() => container?.remove());

  function mount(el: () => any) {
    container = document.createElement("div");
    document.body.appendChild(container);
    render(el, container);
  }

  it("PanelSkeleton renders the requested row count", () => {
    mount(() => <PanelSkeleton rows={4} />);
    const panel = container.querySelector('[data-testid="skeleton-panel"]')!;
    expect(panel.children.length).toBe(4);
  });

  it("ListSkeleton renders the requested row count", () => {
    mount(() => <ListSkeleton rows={3} />);
    const list = container.querySelector('[data-testid="skeleton-list"]')!;
    expect(list.children.length).toBe(3);
  });

  it("TreeSkeleton renders the requested row count", () => {
    mount(() => <TreeSkeleton rows={5} />);
    const tree = container.querySelector('[data-testid="skeleton-tree"]')!;
    expect(tree.children.length).toBe(5);
  });
});
