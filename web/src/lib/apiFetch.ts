// Single place all API calls flow through: typed errors, a 5xx toast, and
// AbortController wiring so callers get "no stale renders" for free.
import { notificationsStore } from "../stores/notifications";

export class ApiError extends Error {
  readonly status: number;
  readonly body: string;
  constructor(status: number, body: string) {
    super(body || `HTTP ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
}

export interface ApiFetchOptions extends RequestInit {
  // Callers with their own inline error UI (e.g. CanvasHost's retry panel)
  // opt out of the automatic toast to avoid double-reporting the same error.
  silent?: boolean;
}

function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === "AbortError";
}

function reportError(input: string, status: number, detail: string): void {
  notificationsStore.add({
    id: `apierr-${Date.now()}-${Math.random().toString(36).slice(2)}`,
    kind: "error",
    message: status > 0 ? `Request failed (${status}): ${input}` : `Network error: ${input}`,
    detail,
  });
}

export async function apiFetch(input: string, init: ApiFetchOptions = {}): Promise<Response> {
  const { silent, ...rest } = init;
  let res: Response;
  try {
    res = await fetch(input, rest);
  } catch (err) {
    if (isAbort(err)) throw err;
    const message = err instanceof Error ? err.message : String(err);
    if (!silent) reportError(input, 0, message);
    throw new ApiError(0, message);
  }
  if (!res.ok) {
    const body = await res.text().catch(() => "");
    if (!silent && res.status >= 500) reportError(input, res.status, body);
    throw new ApiError(res.status, body);
  }
  return res;
}

export async function apiFetchJSON<T = unknown>(input: string, init?: ApiFetchOptions): Promise<T> {
  const res = await apiFetch(input, init);
  return (await res.json()) as T;
}
