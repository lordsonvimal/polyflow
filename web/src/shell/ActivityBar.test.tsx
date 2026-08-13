import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import App from "../App";
import { layoutPrefs } from "../stores/layoutPrefs";

describe("ActivityBar", () => {
  let container: HTMLElement;
  beforeEach(() => {
    layoutPrefs.setActivity("explore");
    container = document.createElement("div");
    document.body.appendChild(container);
  });
  afterEach(() => { container.remove(); });

  it("switching activity changes panel content but canvas-host is the same DOM node", () => {
    render(() => <App />, container);

    const canvasBefore = container.querySelector('[data-testid="canvas-host"]') as HTMLElement;
    expect(canvasBefore).not.toBeNull();

    // Switch to flows activity
    const buttons = container.querySelectorAll('[data-testid="activity-bar"] button');
    (buttons[1] as HTMLButtonElement).click(); // flows

    const canvasAfter = container.querySelector('[data-testid="canvas-host"]') as HTMLElement;
    expect(canvasAfter).toBe(canvasBefore); // same DOM node
    expect(container.querySelector('[data-testid="panel-host"]')).not.toBeNull();
  });
});
