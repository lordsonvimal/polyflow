import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import Toasts from "./Toasts";
import { notificationsStore } from "../stores/notifications";

describe("Toasts", () => {
  let container: HTMLElement;
  beforeEach(() => {
    notificationsStore.clear();
    container = document.createElement("div");
    document.body.appendChild(container);
  });
  afterEach(() => {
    container.remove();
  });

  it("renders an error toast with a verbatim detail expander", () => {
    notificationsStore.add({ kind: "error", message: "Save failed", detail: "422: services[1].path does not exist" });
    render(() => <Toasts />, container);

    const toast = container.querySelector('[data-testid="toast"]')!;
    expect(toast).not.toBeNull();
    expect(toast.textContent).toContain("Save failed");
    expect(container.querySelector('[data-testid="toast-detail"]')).toBeNull();

    (container.querySelector('[data-testid="toast-expand"]') as HTMLButtonElement).click();
    const detail = container.querySelector('[data-testid="toast-detail"]');
    expect(detail).not.toBeNull();
    expect(detail!.textContent).toBe("422: services[1].path does not exist");
  });

  it("dismiss button removes the toast", () => {
    const id = notificationsStore.add({ kind: "error", message: "boom" });
    render(() => <Toasts />, container);
    expect(container.querySelectorAll('[data-testid="toast"]').length).toBe(1);

    (container.querySelector('[data-testid="toast-dismiss"]') as HTMLButtonElement).click();
    expect(notificationsStore.toasts().find((t) => t.id === id)).toBeUndefined();
  });
});
