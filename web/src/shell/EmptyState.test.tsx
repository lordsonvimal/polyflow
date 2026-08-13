import { render } from "solid-js/web";
import { describe, it, expect, afterEach } from "vitest";
import EmptyState, { NoIndexEmptyState, EmptyScopeEmptyState, NoSearchResultsEmptyState } from "./EmptyState";

describe("EmptyState", () => {
  let container: HTMLElement;
  afterEach(() => container?.remove());

  function mount(el: () => any) {
    container = document.createElement("div");
    document.body.appendChild(container);
    render(el, container);
  }

  it("renders the message and invokes the action on click", () => {
    let clicked = false;
    mount(() => <EmptyState message="Nothing here" action={{ label: "Do it", onClick: () => (clicked = true) }} />);
    expect(container.textContent).toContain("Nothing here");
    (container.querySelector('[data-testid="empty-state-action"]') as HTMLButtonElement).click();
    expect(clicked).toBe(true);
  });

  it("renders without an action slot", () => {
    mount(() => <EmptyState message="Nothing here" />);
    expect(container.querySelector('[data-testid="empty-state-action"]')).toBeNull();
  });

  it("NoIndexEmptyState wires Run index", () => {
    let ran = false;
    mount(() => <NoIndexEmptyState onRunIndex={() => (ran = true)} />);
    expect(container.textContent).toContain("No index yet");
    (container.querySelector('[data-testid="empty-state-action"]') as HTMLButtonElement).click();
    expect(ran).toBe(true);
  });

  it("NoIndexEmptyState falls back to CLI instructions with no handler", () => {
    mount(() => <NoIndexEmptyState />);
    expect(container.textContent).toContain("polyflow index");
    expect(container.querySelector('[data-testid="empty-state-action"]')).toBeNull();
  });

  it("EmptyScopeEmptyState wires reset", () => {
    let reset = false;
    mount(() => <EmptyScopeEmptyState onReset={() => (reset = true)} />);
    expect(container.textContent).toContain("no elements");
    (container.querySelector('[data-testid="empty-state-action"]') as HTMLButtonElement).click();
    expect(reset).toBe(true);
  });

  it("NoSearchResultsEmptyState shows the query", () => {
    mount(() => <NoSearchResultsEmptyState query="sync" />);
    expect(container.textContent).toContain('"sync"');
  });
});
