import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { notificationsStore } from "./notifications";

describe("notificationsStore", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    notificationsStore.clear();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("error toasts persist until dismissed (no auto-dismiss)", () => {
    notificationsStore.add({ kind: "error", message: "boom", detail: "stack trace here" });
    vi.advanceTimersByTime(60_000);
    expect(notificationsStore.toasts().length).toBe(1);
    expect(notificationsStore.toasts()[0].message).toBe("boom");
    expect(notificationsStore.toasts()[0].detail).toBe("stack trace here");
  });

  it("info/success toasts auto-dismiss", () => {
    notificationsStore.add({ kind: "success", message: "done" });
    expect(notificationsStore.toasts().length).toBe(1);
    vi.advanceTimersByTime(6000);
    expect(notificationsStore.toasts().length).toBe(0);
  });

  it("dismiss removes a toast by id", () => {
    const id = notificationsStore.add({ kind: "error", message: "boom" });
    notificationsStore.dismiss(id);
    expect(notificationsStore.toasts().length).toBe(0);
  });

  it("clear removes all toasts", () => {
    notificationsStore.add({ kind: "error", message: "a" });
    notificationsStore.add({ kind: "info", message: "b" });
    notificationsStore.clear();
    expect(notificationsStore.toasts().length).toBe(0);
  });
});
