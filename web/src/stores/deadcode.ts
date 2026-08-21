// UI counterpart of `polyflow deadcode` / the MCP `deadcode` tool: GET
// /api/deadcode?service=&file= (internal/server/deadcodeapi.go), backed by
// internal/deadcode.Build. A re-index changes every result here, same as
// healthStore, so it re-fetches on graph_updated/job_done rather than only
// on manual filter changes.
import { createSignal } from "solid-js";
import { apiFetchJSON } from "../lib/apiFetch";
import { connectionStore } from "./connection";

export interface DeadcodeItem {
  id: string;
  label: string;
  type: string;
  service: string;
  file: string;
  line: number;
  end_line?: number;
}

export interface DeadcodeResult {
  functions: DeadcodeItem[];
  total: number;
}

const [data, setData] = createSignal<DeadcodeResult | null>(null);
const [loading, setLoading] = createSignal(false);
const [error, setError] = createSignal<string | null>(null);
const [service, setService] = createSignal("");
const [file, setFile] = createSignal("");

async function load(): Promise<void> {
  setLoading(true);
  setError(null);
  try {
    const p = new URLSearchParams();
    if (service()) p.set("service", service());
    if (file()) p.set("file", file());
    const resp = await apiFetchJSON<DeadcodeResult>(`/api/deadcode?${p}`, { silent: true });
    setData(resp);
  } catch (err) {
    setError(err instanceof Error ? err.message : String(err));
  } finally {
    setLoading(false);
  }
}

export const deadcodeStore = {
  data,
  loading,
  error,
  service,
  setService,
  file,
  setFile,
  load,
  // Test-only: module-level singleton, matching healthStore.reset's pattern.
  reset: () => {
    setData(null);
    setLoading(false);
    setError(null);
    setService("");
    setFile("");
  },
};

connectionStore.onEvent((ev) => {
  if (ev.type !== "graph_updated" && ev.type !== "job_done") return;
  void load();
});
