import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import App from "./App";

describe("App shell", () => {
  let container: HTMLElement;
  beforeEach(() => { container = document.createElement("div"); document.body.appendChild(container); });
  afterEach(() => { container.remove(); });

  it("renders all 6 shell regions", () => {
    render(() => <App />, container);
    expect(container.querySelector('[data-testid="activity-bar"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="top-bar"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="panel-host"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="canvas-host"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="detail-host"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="bottom-drawer"]')).not.toBeNull();
  });
});
