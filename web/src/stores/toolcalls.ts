// UO.1: Tool-call log — a live, searchable, clearable record of every MCP/
// CLI/UI call (UB.2's ops.db, /api/toolcalls, and the tool_call/
// tool_call_evicted SSE events audit.go already broadcasts). This store
// owns the filtered page, the live tail (with pause/buffer/flush), eviction,
// clear-all, and the "possible gap" divider shown after an SSE reconnect.
import { createSignal } from "solid-js";
import { apiFetch, apiFetchJSON } from "../lib/apiFetch";
import { downloadText } from "../lib/export";
import { connectionStore } from "./connection";
import { notificationsStore } from "./notifications";

export interface ProfileStats {
  alloc_bytes: number;
  total_alloc_bytes: number;
  heap_objects: number;
  gc_count: number;
  has_cpu_profile: boolean;
}

export interface ToolCallRow {
  id: number;
  ts: string;
  source: string; // "mcp" | "cli" | "ui"
  tool: string;
  params: string; // JSON
  duration_ms: number;
  status: string; // "ok" | "error"
  error: string;
  result: string; // JSON or raw text, full (capped at 64KiB — see result_truncated)
  result_bytes: number;
  result_truncated: boolean;
  profile: ProfileStats;
}

export interface ToolCallCounts {
  source: Record<string, number>;
  status: Record<string, number>;
}

export interface ToolCallFilters {
  source: string; // "" | mcp | cli | ui
  tool: string;
  status: string; // "" | ok | error
  time: "15m" | "1h" | "24h" | "all";
  q: string;
}

const DEFAULT_FILTERS: ToolCallFilters = { source: "", tool: "", status: "", time: "all", q: "" };
const PAGE_LIMIT = 50;

const [filters, setFiltersRaw] = createSignal<ToolCallFilters>({ ...DEFAULT_FILTERS });
const [rows, setRows] = createSignal<ToolCallRow[]>([]);
const [total, setTotal] = createSignal(0);
// grandTotal: every stored row, ignoring the active filters. counts: per-facet
// breakdowns (each computed with the other filters applied) so each source/
// status chip can show how many rows picking it would surface. Both come from
// GET /api/toolcalls and are kept roughly live between fetches — exact again on
// the next filter change or page load.
const [grandTotal, setGrandTotal] = createSignal(0);
const [counts, setCounts] = createSignal<ToolCallCounts>({ source: {}, status: {} });
const [page, setPage] = createSignal(1);
const [loading, setLoading] = createSignal(false);
const [paused, setPaused] = createSignal(false);
const [bufferedCount, setBufferedCount] = createSignal(0);
// The id to render a "possible gap" divider directly above; -1 means "at
// the bottom of what's currently loaded" (the gap is beyond the fetched page).
const [gapBeforeId, setGapBeforeId] = createSignal<number | null>(null);

let buffer: ToolCallRow[] = [];
let maxSeenId = 0;

function sinceParam(time: ToolCallFilters["time"]): string | undefined {
  if (time === "all") return undefined;
  const ms = { "15m": 15 * 60_000, "1h": 60 * 60_000, "24h": 24 * 60 * 60_000 }[time];
  return new Date(Date.now() - ms).toISOString();
}

function buildParams(f: ToolCallFilters, pageN: number): URLSearchParams {
  const p = new URLSearchParams();
  if (f.source) p.set("source", f.source);
  if (f.tool) p.set("tool", f.tool);
  if (f.status) p.set("status", f.status);
  if (f.q) p.set("q", f.q);
  const since = sinceParam(f.time);
  if (since) p.set("since", since);
  p.set("page", String(pageN));
  p.set("limit", String(PAGE_LIMIT));
  return p;
}

function errDetail(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

async function fetchPage(pageN: number, opts: { markGapIfNonContiguous?: boolean } = {}): Promise<void> {
  setLoading(true);
  const prevMax = maxSeenId;
  try {
    const data = await apiFetchJSON<{
      calls: ToolCallRow[];
      total: number;
      page: number;
      grand_total?: number;
      counts?: ToolCallCounts;
    }>(`/api/toolcalls?${buildParams(filters(), pageN)}`, { silent: true });
    const calls = data.calls ?? [];
    if (pageN === 1) {
      setRows(calls);
      if (opts.markGapIfNonContiguous) {
        setGapBeforeId(computeGapDivider(calls, prevMax));
      } else {
        setGapBeforeId(null);
      }
    } else {
      setRows((r) => [...r, ...calls]);
    }
    if (calls.length > 0) maxSeenId = Math.max(maxSeenId, calls[0].id);
    setTotal(data.total ?? 0);
    setPage(data.page ?? pageN);
    setGrandTotal(data.grand_total ?? data.total ?? 0);
    setCounts(data.counts ?? { source: {}, status: {} });
  } catch (err) {
    notificationsStore.add({
      id: `toolcalls-fetch-err-${Date.now()}`,
      kind: "error",
      message: "Failed to load tool calls",
      detail: errDetail(err),
    });
  } finally {
    setLoading(false);
  }
}

// Divider placement: null = contiguous (no gap); a row id = render the
// divider directly above that row; -1 = the gap is older than anything the
// fetched page returned (render the divider after the whole list).
function computeGapDivider(calls: ToolCallRow[], prevMax: number): number | null {
  if (prevMax <= 0 || calls.length === 0) return null;
  const newest = calls[0].id;
  if (newest <= prevMax + 1) return null;
  const boundary = calls.find((c) => c.id <= prevMax);
  return boundary ? boundary.id : -1;
}

function setFilters(f: Partial<ToolCallFilters>): void {
  setFiltersRaw((prev) => ({ ...prev, ...f }));
  setGapBeforeId(null);
  void fetchPage(1);
}

function resetFilters(): void {
  setFiltersRaw({ ...DEFAULT_FILTERS });
  void fetchPage(1);
}

function loadInitial(): void {
  void fetchPage(1);
}

function loadMore(): void {
  if (loading()) return;
  if (rows().length >= total()) return;
  void fetchPage(page() + 1);
}

function matchesFilters(row: ToolCallRow, f: ToolCallFilters): boolean {
  if (f.source && row.source !== f.source) return false;
  if (f.tool && row.tool !== f.tool) return false;
  if (f.status && row.status !== f.status) return false;
  const since = sinceParam(f.time);
  if (since && row.ts < since) return false;
  if (f.q) {
    const needle = f.q.toLowerCase();
    if (
      !row.params.toLowerCase().includes(needle) &&
      !row.result.toLowerCase().includes(needle) &&
      !row.error.toLowerCase().includes(needle)
    ) {
      return false;
    }
  }
  return true;
}

function prepend(row: ToolCallRow): void {
  maxSeenId = Math.max(maxSeenId, row.id);
  setRows((r) => [row, ...r]);
  setTotal((t) => t + 1);
}

// Keep the facet counts roughly live for a row, whether or not it matches the
// active filter (the counts are filter-independent tallies). Faceting drift
// from eviction is tolerated — the next fetchPage corrects it.
function bumpFacets(row: ToolCallRow, delta: number): void {
  setCounts((c) => ({
    source: { ...c.source, [row.source]: Math.max(0, (c.source[row.source] ?? 0) + delta) },
    status: { ...c.status, [row.status]: Math.max(0, (c.status[row.status] ?? 0) + delta) },
  }));
}

function togglePause(): void {
  setPaused((p) => {
    const next = !p;
    if (!next) flushBuffer();
    return next;
  });
}

function flushBuffer(): void {
  if (buffer.length === 0) return;
  const flushed = buffer;
  buffer = [];
  setBufferedCount(0);
  setRows((r) => [...flushed, ...r]);
  setTotal((t) => t + flushed.length);
}

function handleToolCall(call: ToolCallRow): void {
  setGrandTotal((t) => t + 1);
  bumpFacets(call, +1);
  if (!matchesFilters(call, filters())) return;
  if (paused()) {
    buffer = [call, ...buffer];
    setBufferedCount(buffer.length);
    maxSeenId = Math.max(maxSeenId, call.id);
    return;
  }
  prepend(call);
}

function handleEvicted(ids: number[]): void {
  const idSet = new Set(ids);
  // Adjust facet counts for the evicted rows we actually hold (source/status
  // known); grandTotal drops by the full id count regardless. Any residual
  // facet drift is corrected on the next fetchPage.
  [...rows(), ...buffer].filter((row) => idSet.has(row.id)).forEach((row) => bumpFacets(row, -1));
  setGrandTotal((t) => Math.max(0, t - ids.length));
  setRows((r) => r.filter((row) => !idSet.has(row.id)));
  if (buffer.length > 0) {
    buffer = buffer.filter((row) => !idSet.has(row.id));
    setBufferedCount(buffer.length);
  }
  setTotal((t) => Math.max(0, t - ids.length));
}

async function clearAll(): Promise<void> {
  try {
    await apiFetch("/api/toolcalls", { method: "DELETE", silent: true });
    setRows([]);
    setTotal(0);
    setGrandTotal(0);
    setCounts({ source: {}, status: {} });
    setPage(1);
    buffer = [];
    setBufferedCount(0);
    maxSeenId = 0;
    setGapBeforeId(null);
  } catch (err) {
    notificationsStore.add({
      id: `toolcalls-clear-err-${Date.now()}`,
      kind: "error",
      message: "Failed to clear tool-call log",
      detail: errDetail(err),
    });
  }
}

function downloadFiltered(): void {
  downloadText(
    `polyflow-toolcalls-${new Date().toISOString().slice(0, 10)}.json`,
    JSON.stringify(rows(), null, 2),
    "application/json",
  );
}

// Distinct tool values seen across loaded rows — the filter dropdown's
// options (MCP tool names, CLI command paths, UI route patterns), per the
// spec's "distinct values from loaded rows" (not a separate endpoint). A
// plain function (recomputed per read) rather than a module-level
// createMemo, which would create a computation with no owning root.
function toolOptions(): string[] {
  const set = new Set<string>();
  rows().forEach((r) => set.add(r.tool));
  return Array.from(set).sort();
}

connectionStore.onEvent((ev) => {
  if (ev.type === "tool_call") {
    const call = (ev as { call?: ToolCallRow }).call;
    if (call) handleToolCall(call);
  } else if (ev.type === "tool_call_evicted") {
    const ids = (ev as { ids?: number[] }).ids;
    if (ids && ids.length > 0) handleEvicted(ids);
  }
});

// On reconnect the stream may have dropped messages while it was down; a
// non-contiguous newest id after refetch is the only honest signal of that
// (per UB.2's audit trust contract — never claim completeness silently).
connectionStore.onReconnect(() => {
  void fetchPage(1, { markGapIfNonContiguous: true });
});

export const toolCallsStore = {
  filters,
  rows,
  total,
  grandTotal,
  counts,
  loading,
  paused,
  bufferedCount,
  gapBeforeId,
  toolOptions,
  setFilters,
  resetFilters,
  loadInitial,
  loadMore,
  togglePause,
  flushBuffer,
  clearAll,
  downloadFiltered,
  // Test-only: module-level singleton, mirrors jobsStore.reset.
  reset: () => {
    setFiltersRaw({ ...DEFAULT_FILTERS });
    setRows([]);
    setTotal(0);
    setGrandTotal(0);
    setCounts({ source: {}, status: {} });
    setPage(1);
    setLoading(false);
    setPaused(false);
    setBufferedCount(0);
    setGapBeforeId(null);
    buffer = [];
    maxSeenId = 0;
  },
};
