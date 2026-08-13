import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { apiFetch, apiFetchJSON, ApiError } from "./apiFetch";
import { notificationsStore } from "../stores/notifications";

describe("apiFetch", () => {
  const originalFetch = global.fetch;
  beforeEach(() => {
    notificationsStore.clear();
  });
  afterEach(() => {
    global.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it("returns the response on a 2xx", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
    const res = await apiFetch("/api/thing");
    expect(res.status).toBe(200);
  });

  it("throws a typed ApiError with the verbatim body on non-2xx", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response("boom: bad workspace path", { status: 500 }));
    await expect(apiFetch("/api/thing")).rejects.toMatchObject({
      status: 500,
      body: "boom: bad workspace path",
    });
  });

  it("raises a persistent error toast with the verbatim body on 5xx", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response("server exploded", { status: 503 }));
    await expect(apiFetch("/api/thing")).rejects.toBeInstanceOf(ApiError);
    const toasts = notificationsStore.toasts();
    expect(toasts.length).toBe(1);
    expect(toasts[0].kind).toBe("error");
    expect(toasts[0].detail).toBe("server exploded");
  });

  it("does not toast on 4xx", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response("not found", { status: 404 }));
    await expect(apiFetch("/api/thing")).rejects.toBeInstanceOf(ApiError);
    expect(notificationsStore.toasts().length).toBe(0);
  });

  it("silent option suppresses the toast even on 5xx", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response("server exploded", { status: 500 }));
    await expect(apiFetch("/api/thing", { silent: true })).rejects.toBeInstanceOf(ApiError);
    expect(notificationsStore.toasts().length).toBe(0);
  });

  it("re-throws AbortError without toasting", async () => {
    const abortErr = new DOMException("aborted", "AbortError");
    global.fetch = vi.fn().mockRejectedValue(abortErr);
    await expect(apiFetch("/api/thing")).rejects.toBe(abortErr);
    expect(notificationsStore.toasts().length).toBe(0);
  });

  it("apiFetchJSON parses the body", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const data = await apiFetchJSON<{ ok: boolean }>("/api/thing");
    expect(data.ok).toBe(true);
  });
});
