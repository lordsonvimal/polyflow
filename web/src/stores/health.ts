// UO.3: Health & trust dashboard — GET /api/health (UB.5's backend, already
// complete). This store owns the fetch + auto-refresh on graph_updated/
// job_done (a re-index changes every number this page shows).
import { createSignal } from "solid-js";
import { apiFetchJSON } from "../lib/apiFetch";
import { connectionStore } from "./connection";

export interface ParseErrorEntry {
  file_path: string;
  service: string;
  error_count: number;
  first_error_line: number;
  indexed_at: number;
}

export interface HealthIndex {
  indexed_at: string;
  schema_version: string;
  nodes: number;
  edges: number;
  parse_errors: number;
  parse_error_list: ParseErrorEntry[];
}

export interface HealthCoverage {
  verified: number;
  candidate: number;
  observed_only_gap: number;
  conflicting: number;
  stale_evidence?: number;
  note?: string;
}

export interface HealthEvalRepo {
  name: string;
  recall: number;
}

export interface HealthEval {
  present: boolean;
  repos?: HealthEvalRepo[];
}

export interface HealthTrust {
  measured: boolean;
  corpus?: string;
  cases?: number;
  recall?: number;
  hard_fails?: number;
  silent_misses?: number;
  measured_at?: string;
  stale?: boolean;
}

export interface HealthData {
  index: HealthIndex;
  coverage: HealthCoverage;
  unresolved_total: number;
  unresolved_by_kind: Record<string, number>;
  eval: HealthEval;
  trust: HealthTrust;
}

const [data, setData] = createSignal<HealthData | null>(null);
const [loading, setLoading] = createSignal(false);
const [error, setError] = createSignal<string | null>(null);

async function load(): Promise<void> {
  setLoading(true);
  setError(null);
  try {
    const resp = await apiFetchJSON<HealthData>("/api/health", { silent: true });
    setData(resp);
  } catch (err) {
    setError(err instanceof Error ? err.message : String(err));
  } finally {
    setLoading(false);
  }
}

export const healthStore = {
  data,
  loading,
  error,
  load,
  // Test-only: module-level singleton, matching jobsStore.reset's pattern.
  reset: () => {
    setData(null);
    setLoading(false);
    setError(null);
  },
};

// Wired once at module load, matching jobsStore/toolcallsStore's pattern —
// a re-index (job_done) or any other graph mutation (graph_updated) makes
// this page's numbers stale.
connectionStore.onEvent((ev) => {
  if (ev.type !== "graph_updated" && ev.type !== "job_done") return;
  void load();
});
