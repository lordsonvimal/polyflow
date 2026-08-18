// UO.6: Runtime tab's data layer — a session's observed flow records +
// ingest ledger (/api/runtime/flows) and its coverage breakdown against the
// static graph (/api/runtime/coverage), plus the "propose contract rule"
// preview (/api/reconcile/propose) for observed-only gap rows.
import { createSignal } from "solid-js";
import { apiFetchJSON, ApiError } from "../lib/apiFetch";
import { notificationsStore } from "./notifications";

// internal/evidence/trace_ingest has no json tags on any of these structs,
// so the wire shape is Go's exported field names verbatim.
export interface RuntimeSpan {
  TraceID: string;
  SpanID: string;
  ParentSpanID: string;
  Kind: string;
  Service: string;
  Name: string;
  StartUnixNano: number;
  EndUnixNano: number;
  Attrs?: Record<string, string>;
}

export interface RuntimeFlowRecord {
  Kind: string;
  Key: string;
  FromService: string;
  ToService: string;
  Causality: string;
}

export interface RuntimeLedgerEntry {
  Session: string;
  TraceID: string;
  SpanID: string;
  Service: string;
  Reason: string;
}

export interface CoverageRow {
  Kind: string;
  Total: number;
  Verified: number;
  Candidate: number;
  Gap: number;
  Pct: number;
}

export interface ObservedOnlyGap {
  Kind: string;
  Key: string;
  From: string;
  To: string;
}

export interface CoverageReport {
  Rows: CoverageRow[];
  TotalChannels: number;
  VerifiedChannels: number;
  CandidateChannels: number;
  GapChannels: number;
  LedgerByReason: Record<string, number>;
  ObservedOnlyGaps: ObservedOnlyGap[];
}

export interface Proposal {
  filename: string;
  content: string;
}

const [loading, setLoading] = createSignal(false);
const [error, setError] = createSignal<string | null>(null);
const [spans, setSpans] = createSignal<RuntimeSpan[]>([]);
const [flowRecords, setFlowRecords] = createSignal<RuntimeFlowRecord[]>([]);
const [ledger, setLedger] = createSignal<RuntimeLedgerEntry[]>([]);
const [coverage, setCoverage] = createSignal<CoverageReport | null>(null);
const [proposals, setProposals] = createSignal<Record<string, Proposal>>({});

function gapKey(g: ObservedOnlyGap): string {
  return `${g.Kind}|${g.Key}|${g.From}|${g.To}`;
}

function errMessage(err: unknown): string {
  if (err instanceof ApiError) return err.body || err.message;
  return err instanceof Error ? err.message : String(err);
}

async function load(session: string): Promise<void> {
  setLoading(true);
  setError(null);
  try {
    const [flows, cov] = await Promise.all([
      apiFetchJSON<{ spans: RuntimeSpan[]; flow_records: RuntimeFlowRecord[]; ledger: RuntimeLedgerEntry[] }>(
        `/api/runtime/flows?session=${encodeURIComponent(session)}`,
        { silent: true }
      ),
      apiFetchJSON<{ coverage: CoverageReport }>(`/api/runtime/coverage?session=${encodeURIComponent(session)}`, {
        silent: true,
      }),
    ]);
    setSpans(flows.spans ?? []);
    setFlowRecords(flows.flow_records ?? []);
    setLedger(flows.ledger ?? []);
    setCoverage(cov.coverage);
  } catch (err) {
    setError(errMessage(err));
  } finally {
    setLoading(false);
  }
}

async function proposeRule(gap: ObservedOnlyGap): Promise<Proposal | null> {
  const key = gapKey(gap);
  const existing = proposals()[key];
  if (existing) return existing;
  try {
    const params = new URLSearchParams({ kind: gap.Kind, key: gap.Key, from: gap.From, to: gap.To });
    const data = await apiFetchJSON<Proposal>(`/api/reconcile/propose?${params.toString()}`, { silent: true });
    setProposals((p) => ({ ...p, [key]: data }));
    return data;
  } catch (err) {
    notificationsStore.add({
      id: `runtime-propose-err-${Date.now()}`,
      kind: "error",
      message: "Failed to generate proposal",
      detail: errMessage(err),
    });
    return null;
  }
}

export const runtimeStore = {
  loading,
  error,
  spans,
  flowRecords,
  ledger,
  coverage,
  proposals,
  gapKey,
  load,
  proposeRule,
  reset: () => {
    setLoading(false);
    setError(null);
    setSpans([]);
    setFlowRecords([]);
    setLedger([]);
    setCoverage(null);
    setProposals({});
  },
};
