// Ops/UI settings (internal/server/toolcalls.go's GET/PUT /api/settings) —
// currently just the tool-call log's retention cap.
import { createSignal } from "solid-js";
import { apiFetch, apiFetchJSON, ApiError } from "../lib/apiFetch";

export const MIN_RETENTION = 1;
export const MAX_RETENTION = 10000;

const [toolCallRetention, setToolCallRetention] = createSignal<number | undefined>(undefined);
const [loading, setLoading] = createSignal(false);
const [saving, setSaving] = createSignal(false);
const [error, setError] = createSignal<string | undefined>(undefined);

async function load(): Promise<void> {
  setLoading(true);
  setError(undefined);
  try {
    const data = await apiFetchJSON<{ tool_call_retention: number }>("/api/settings");
    setToolCallRetention(data.tool_call_retention);
  } catch (err) {
    setError(err instanceof Error ? err.message : String(err));
  } finally {
    setLoading(false);
  }
}

// setRetention returns the server's rejection message (field-specific, from
// the 422 body) on failure rather than throwing, so the caller can show it
// inline next to the input instead of only as a toast.
async function setRetention(n: number): Promise<string | undefined> {
  setSaving(true);
  try {
    const r = await apiFetch("/api/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tool_call_retention: n }),
      silent: true,
    });
    const data = (await r.json()) as { tool_call_retention: number };
    setToolCallRetention(data.tool_call_retention);
    return undefined;
  } catch (err) {
    if (err instanceof ApiError && err.status === 422) {
      try {
        return (JSON.parse(err.body) as { error?: string }).error ?? err.message;
      } catch {
        return err.message;
      }
    }
    return err instanceof Error ? err.message : String(err);
  } finally {
    setSaving(false);
  }
}

export const settingsStore = {
  toolCallRetention,
  loading,
  saving,
  error,
  load,
  setRetention,
};
