import { For, Show, createMemo, createResource, createSignal, onMount } from "solid-js";
import { toolCallsStore, type ToolCallFilters, type ToolCallRow } from "../../stores/toolcalls";
import { apiFetch } from "../../lib/apiFetch";
import { downloadText } from "../../lib/export";
import { notificationsStore } from "../../stores/notifications";
import { scopeStore } from "../../stores/scope";
import { selectionStore } from "../../stores/selection";
import { treeStore } from "../../stores/tree";

const SOURCE_LABEL: Record<string, string> = { mcp: "MCP", cli: "CLI", ui: "UI" };

// Canonical source colors — no other view badges "source" yet, so this table
// is the one other future call sites (spec: "consistent with the source
// colors used everywhere else the source appears") should match.
const SOURCE_BADGE: Record<string, string> = {
  mcp: "bg-purple-900/50 text-purple-300",
  cli: "bg-emerald-900/50 text-emerald-300",
  ui: "bg-sky-900/50 text-sky-300",
};

const SOURCES: ("mcp" | "cli" | "ui")[] = ["mcp", "cli", "ui"];
const TIME_PRESETS: ToolCallFilters["time"][] = ["15m", "1h", "24h", "all"];

function durationColor(ms: number): string {
  if (ms > 5000) return "text-red-400";
  if (ms > 1000) return "text-amber-400";
  return "text-neutral-400";
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  return `${(n / 1024).toFixed(1)} KB`;
}

function relativeTime(ts: string): string {
  const t = Date.parse(ts);
  if (Number.isNaN(t)) return ts;
  const s = Math.floor((Date.now() - t) / 1000);
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function prettyPrint(raw: string): string {
  if (!raw) return "";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

// The `q` filter's highlighted-match view (mark tags), applied to whatever
// text a pane is showing — pretty-printed JSON or raw text alike.
function HighlightedText(props: { text: string; needle: string }) {
  const segments = createMemo(() => {
    const text = props.text;
    const needle = props.needle.trim();
    if (!needle) return [{ text, hl: false }];
    const lower = text.toLowerCase();
    const n = needle.toLowerCase();
    const out: { text: string; hl: boolean }[] = [];
    let i = 0;
    while (i < text.length) {
      const idx = lower.indexOf(n, i);
      if (idx === -1) {
        out.push({ text: text.slice(i), hl: false });
        break;
      }
      if (idx > i) out.push({ text: text.slice(i, idx), hl: false });
      out.push({ text: text.slice(idx, idx + n.length), hl: true });
      i = idx + n.length;
    }
    return out;
  });
  return (
    <For each={segments()}>
      {(seg) => (seg.hl ? <mark class="bg-amber-600/60 text-white rounded-sm">{seg.text}</mark> : <>{seg.text}</>)}
    </For>
  );
}

// UN.2 behavior: candidate node ids embedded in a JSON blob (quoted strings
// shaped like a node id — colon-separated segments) resolved on demand via
// GET /api/node/{id}; only ids that actually resolve become links.
const ID_CANDIDATE_RE = /"([A-Za-z0-9_./#-]+:[A-Za-z0-9_./#-]+:[A-Za-z0-9_./#-]+(?::\d+)?)"/g;

function extractCandidates(text: string): string[] {
  const set = new Set<string>();
  let m: RegExpExecArray | null;
  ID_CANDIDATE_RE.lastIndex = 0;
  while ((m = ID_CANDIDATE_RE.exec(text)) && set.size < 25) {
    set.add(m[1]);
  }
  return Array.from(set);
}

interface ResolvedNode {
  id: string;
  service: string;
  file: string;
}

async function resolveCandidates(text: string): Promise<ResolvedNode[]> {
  const candidates = extractCandidates(text);
  if (candidates.length === 0) return [];
  const results = await Promise.all(
    candidates.map(async (id) => {
      try {
        const r = await apiFetch(`/api/node/${encodeURIComponent(id)}`, { silent: true });
        const body = (await r.json()) as { node?: { id: string; service: string; file: string } };
        return body.node ? { id: body.node.id, service: body.node.service, file: body.node.file } : null;
      } catch {
        return null;
      }
    }),
  );
  return results.filter((n): n is ResolvedNode => n !== null);
}

function jumpToNode(n: ResolvedNode): void {
  if (n.file) {
    scopeStore.push({ kind: "file", service: n.service, path: n.file });
  }
  selectionStore.setSelection({ kind: "node", id: n.id });
  treeStore.reveal(n.id);
}

async function copyText(text: string, label: string): Promise<void> {
  try {
    await navigator.clipboard?.writeText(text);
    notificationsStore.add({ id: `toolcalls-copy-${Date.now()}`, kind: "success", message: `${label} copied to clipboard` });
  } catch {
    notificationsStore.add({ id: `toolcalls-copy-err-${Date.now()}`, kind: "error", message: `Could not copy ${label.toLowerCase()}` });
  }
}

function ToolCallRowView(props: { row: ToolCallRow; q: string; expanded: boolean; onToggle: () => void }) {
  const [jumps] = createResource(
    () => (props.expanded ? `${props.row.params}\n${props.row.result}` : null),
    (text) => resolveCandidates(text),
  );

  const inputText = createMemo(() => prettyPrint(props.row.params));
  const outputText = createMemo(() => prettyPrint(props.row.result));

  function downloadTruncated(): void {
    downloadText(`polyflow-toolcall-${props.row.id}-result.txt`, props.row.result, "text/plain");
  }

  return (
    <li data-testid="toolcalls-row" class="border-b border-neutral-900">
      <div
        data-testid="toolcalls-row-summary"
        class="flex items-center gap-2 px-1 py-1 cursor-pointer hover:bg-neutral-900"
        onClick={props.onToggle}
      >
        <span data-testid="toolcalls-row-time" title={props.row.ts} class="text-neutral-500 w-16 shrink-0">
          {relativeTime(props.row.ts)}
        </span>
        <span data-testid="toolcalls-row-source" class={`px-1.5 py-0.5 rounded shrink-0 ${SOURCE_BADGE[props.row.source] ?? "bg-neutral-800 text-neutral-300"}`}>
          {SOURCE_LABEL[props.row.source] ?? props.row.source}
        </span>
        <span data-testid="toolcalls-row-tool" class="text-neutral-200 truncate">
          {props.row.tool}
        </span>
        <span data-testid="toolcalls-row-duration" class={`ml-auto shrink-0 ${durationColor(props.row.duration_ms)}`}>
          {props.row.duration_ms}ms
        </span>
        <span data-testid="toolcalls-row-size" class="text-neutral-500 shrink-0">
          {formatBytes(props.row.result_bytes)}
        </span>
        <span data-testid="toolcalls-row-status" class={`shrink-0 ${props.row.status === "error" ? "text-red-400" : "text-emerald-400"}`}>
          {props.row.status}
        </span>
      </div>

      <Show when={props.expanded}>
        <div data-testid="toolcalls-row-expanded" class="px-2 pb-2 space-y-2">
          <Show when={props.row.result_truncated}>
            <div data-testid="toolcalls-truncated-banner" class="text-amber-400 bg-amber-950/40 rounded p-1.5">
              Showing {formatBytes(props.row.result.length)} of {formatBytes(props.row.result_bytes)} — the full
              output was not retained (only the first 64 KiB is stored server-side).{" "}
              <button data-testid="toolcalls-truncated-download" class="underline hover:text-amber-300" onClick={downloadTruncated}>
                download full
              </button>{" "}
              downloads the stored 64 KiB, not the original {formatBytes(props.row.result_bytes)}.
            </div>
          </Show>

          <Show when={props.row.profile}>
            <div data-testid="toolcalls-profile" class="flex items-center gap-3 text-neutral-500">
              <span title="heap in use at completion">alloc {formatBytes(props.row.profile.alloc_bytes)}</span>
              <span title="cumulative bytes allocated during this call">total alloc {formatBytes(props.row.profile.total_alloc_bytes)}</span>
              <span title="garbage-collector cycles run during this call">gc {props.row.profile.gc_count}</span>
              <Show when={props.row.profile.has_cpu_profile}>
                <a
                  data-testid="toolcalls-profile-download"
                  class="ml-auto text-indigo-300 hover:text-indigo-200 underline"
                  href={`/api/toolcalls/${props.row.id}/profile`}
                  download={`toolcall-${props.row.id}.pprof`}
                >
                  ↓ CPU profile
                </a>
              </Show>
            </div>
          </Show>

          <div class="grid grid-cols-2 gap-2">
            <div data-testid="toolcalls-input-pane" class="min-w-0">
              <div class="flex items-center gap-2 text-neutral-400 mb-0.5">
                <span>Input</span>
                <button data-testid="toolcalls-copy-input" class="ml-auto hover:text-white" onClick={() => copyText(inputText(), "Input")}>
                  copy
                </button>
              </div>
              <pre data-testid="toolcalls-input-json" class="bg-neutral-900 rounded p-1.5 text-[10px] whitespace-pre-wrap break-all max-h-64 overflow-y-auto">
                <HighlightedText text={inputText()} needle={props.q} />
              </pre>
            </div>
            <div data-testid="toolcalls-output-pane" class="min-w-0">
              <div class="flex items-center gap-2 text-neutral-400 mb-0.5">
                <span>Output</span>
                <button data-testid="toolcalls-copy-output" class="ml-auto hover:text-white" onClick={() => copyText(outputText(), "Output")}>
                  copy
                </button>
              </div>
              <Show when={props.row.error}>
                <pre data-testid="toolcalls-error" class="bg-red-950/40 text-red-300 rounded p-1.5 text-[10px] whitespace-pre-wrap break-all mb-1">
                  <HighlightedText text={props.row.error} needle={props.q} />
                </pre>
              </Show>
              <pre data-testid="toolcalls-output-json" class="bg-neutral-900 rounded p-1.5 text-[10px] whitespace-pre-wrap break-all max-h-64 overflow-y-auto">
                <HighlightedText text={outputText()} needle={props.q} />
              </pre>
            </div>
          </div>

          <Show when={jumps() && jumps()!.length > 0}>
            <div data-testid="toolcalls-jump-links" class="flex flex-wrap gap-2 text-indigo-300">
              <For each={jumps()}>
                {(n) => (
                  <button data-testid="toolcalls-jump-link" class="hover:text-indigo-200 underline" onClick={() => jumpToNode(n)}>
                    ↗ {n.id}
                  </button>
                )}
              </For>
            </div>
          </Show>
        </div>
      </Show>
    </li>
  );
}

export default function ToolCallsTab() {
  const [expandedId, setExpandedId] = createSignal<number | null>(null);
  const [confirmingClear, setConfirmingClear] = createSignal(false);
  const [qInput, setQInput] = createSignal("");
  let qTimer: ReturnType<typeof setTimeout> | undefined;

  onMount(() => toolCallsStore.loadInitial());

  function onQInput(v: string): void {
    setQInput(v);
    clearTimeout(qTimer);
    qTimer = setTimeout(() => toolCallsStore.setFilters({ q: v }), 250);
  }

  function toggleSource(s: "mcp" | "cli" | "ui"): void {
    toolCallsStore.setFilters({ source: toolCallsStore.filters().source === s ? "" : s });
  }

  function toggleRow(id: number): void {
    setExpandedId((cur) => (cur === id ? null : id));
  }

  return (
    <div data-testid="toolcalls-tab" class="p-2 text-xs text-neutral-300 flex flex-col h-full gap-2">
      <div class="flex items-center gap-2 flex-wrap shrink-0">
        <div class="flex gap-1">
          <For each={SOURCES}>
            {(s) => (
              <button
                data-testid={`toolcalls-filter-source-${s}`}
                class={`px-1.5 py-0.5 rounded ${toolCallsStore.filters().source === s ? "bg-indigo-600 text-white" : "bg-neutral-800 text-neutral-400 hover:text-white"}`}
                onClick={() => toggleSource(s)}
              >
                {SOURCE_LABEL[s]}
              </button>
            )}
          </For>
        </div>

        <select
          data-testid="toolcalls-filter-tool"
          class="bg-neutral-800 rounded px-1.5 py-0.5"
          value={toolCallsStore.filters().tool}
          onChange={(e) => toolCallsStore.setFilters({ tool: e.currentTarget.value })}
        >
          <option value="">all tools</option>
          <For each={toolCallsStore.toolOptions()}>{(t) => <option value={t}>{t}</option>}</For>
        </select>

        <select
          data-testid="toolcalls-filter-status"
          class="bg-neutral-800 rounded px-1.5 py-0.5"
          value={toolCallsStore.filters().status}
          onChange={(e) => toolCallsStore.setFilters({ status: e.currentTarget.value })}
        >
          <option value="">any status</option>
          <option value="ok">ok</option>
          <option value="error">error</option>
        </select>

        <div class="flex gap-1">
          <For each={TIME_PRESETS}>
            {(t) => (
              <button
                data-testid={`toolcalls-filter-time-${t}`}
                class={`px-1.5 py-0.5 rounded ${toolCallsStore.filters().time === t ? "bg-indigo-600 text-white" : "bg-neutral-800 text-neutral-400 hover:text-white"}`}
                onClick={() => toolCallsStore.setFilters({ time: t })}
              >
                {t}
              </button>
            )}
          </For>
        </div>

        <input
          data-testid="toolcalls-filter-q"
          class="bg-neutral-800 rounded px-1.5 py-0.5 flex-1 min-w-[8rem]"
          placeholder="search input & output…"
          value={qInput()}
          onInput={(e) => onQInput(e.currentTarget.value)}
        />
      </div>

      <div class="flex items-center gap-2 shrink-0">
        <button
          data-testid="toolcalls-pause"
          class="text-neutral-400 hover:text-white"
          onClick={toolCallsStore.togglePause}
        >
          {toolCallsStore.paused() ? "▶ resume" : "⏸ pause"}
        </button>
        <Show when={toolCallsStore.bufferedCount() > 0}>
          <button
            data-testid="toolcalls-buffered-pill"
            class="px-1.5 py-0.5 rounded bg-indigo-600 text-white"
            onClick={toolCallsStore.flushBuffer}
          >
            +{toolCallsStore.bufferedCount()} new
          </button>
        </Show>
        <button data-testid="toolcalls-download" class="text-neutral-400 hover:text-white ml-auto" onClick={toolCallsStore.downloadFiltered}>
          Download
        </button>
        <Show
          when={!confirmingClear()}
          fallback={
            <span class="flex items-center gap-1">
              <span class="text-neutral-400">Clear all {toolCallsStore.total()} calls?</span>
              <button
                data-testid="toolcalls-clear-confirm"
                class="text-red-400 hover:text-red-300"
                onClick={() => {
                  setConfirmingClear(false);
                  void toolCallsStore.clearAll();
                }}
              >
                Yes
              </button>
              <button data-testid="toolcalls-clear-cancel" class="text-neutral-400 hover:text-white" onClick={() => setConfirmingClear(false)}>
                No
              </button>
            </span>
          }
        >
          <button data-testid="toolcalls-clear" class="text-neutral-400 hover:text-red-300" onClick={() => setConfirmingClear(true)}>
            Clear all
          </button>
        </Show>
      </div>

      <div class="flex-1 overflow-y-auto min-h-0">
        <Show when={toolCallsStore.loading() && toolCallsStore.rows().length === 0}>
          <div class="text-neutral-400">Loading…</div>
        </Show>
        <Show when={!toolCallsStore.loading() && toolCallsStore.rows().length === 0}>
          <div data-testid="toolcalls-empty" class="text-neutral-500">
            Log cleared · new calls appear live
          </div>
        </Show>
        <ul data-testid="toolcalls-list">
          <For each={toolCallsStore.rows()}>
            {(row) => (
              <>
                <Show when={toolCallsStore.gapBeforeId() === row.id}>
                  <li data-testid="toolcalls-gap-divider" class="text-center text-amber-500 py-1">
                    — possible gap: the connection dropped, some calls may be missing —
                  </li>
                </Show>
                <ToolCallRowView row={row} q={toolCallsStore.filters().q} expanded={expandedId() === row.id} onToggle={() => toggleRow(row.id)} />
              </>
            )}
          </For>
        </ul>
        <Show when={toolCallsStore.gapBeforeId() === -1 && toolCallsStore.rows().length > 0}>
          <div data-testid="toolcalls-gap-divider" class="text-center text-amber-500 py-1">
            — possible gap: the connection dropped, some calls may be missing —
          </div>
        </Show>
        <Show when={!toolCallsStore.loading() && toolCallsStore.rows().length > 0 && toolCallsStore.rows().length < toolCallsStore.total()}>
          <button data-testid="toolcalls-load-more" class="w-full text-neutral-400 hover:text-white py-1" onClick={toolCallsStore.loadMore}>
            Load more ({toolCallsStore.rows().length}/{toolCallsStore.total()})
          </button>
        </Show>
      </div>
    </div>
  );
}
