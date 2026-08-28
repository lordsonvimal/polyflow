import { For, Show, createResource, createSignal, createEffect } from "solid-js";
import { drawerStore, type DrawerTab } from "../stores/drawer";
import { contextCopyStore, TOKEN_BUDGETS } from "../stores/contextCopy";
import MarkdownPreview from "../lib/MarkdownPreview";
import type { CopyMode } from "../views/context/copy";
import { apiFetch } from "../lib/apiFetch";
import { scopeStore } from "../stores/scope";
import type { UnresolvedRef } from "../stores/tree";
import { CONFIDENCE_LEVELS } from "../lib/confidence";
import JobsTab from "../views/ops/JobsTab";
import ToolCallsTab from "../views/ops/ToolCallsTab";

const TABS: { id: DrawerTab; label: string }[] = [
  { id: "context", label: "Context" },
  { id: "unresolved", label: "Unresolved" },
  { id: "unknown-edges", label: "Unknown Edges" },
  { id: "jobs", label: "Jobs" },
  { id: "toolcalls", label: "Tool Calls" },
];

function ContextTab() {
  const result = contextCopyStore.result;
  const err = contextCopyStore.error;

  return (
    <div data-testid="context-tab" class="flex h-full text-xs">
      <div class="w-48 shrink-0 border-r border-neutral-800 p-2 space-y-3 overflow-y-auto">
        <div>
          <div class="text-neutral-400 mb-1">Mode</div>
          <div class="flex gap-1">
            <For each={["viewed", "expanded"] as CopyMode[]}>
              {(m) => (
                <button
                  data-testid={`context-mode-${m}`}
                  class={`px-2 py-0.5 rounded ${contextCopyStore.mode() === m ? "bg-indigo-600 text-white" : "bg-neutral-800 text-neutral-400 hover:text-white"}`}
                  onClick={() => contextCopyStore.setMode(m)}
                >
                  {m === "viewed" ? "Viewed" : "Expanded"}
                </button>
              )}
            </For>
          </div>
        </div>

        <Show when={contextCopyStore.mode() === "expanded"}>
          <div>
            <div class="text-neutral-400 mb-1">Depth: {contextCopyStore.depth()}</div>
            <input
              data-testid="context-depth"
              type="range"
              min="1"
              max="5"
              value={contextCopyStore.depth()}
              onInput={(e) => contextCopyStore.setDepth(Number(e.currentTarget.value))}
              class="w-full"
            />
          </div>
        </Show>

        <label class="flex items-center gap-1.5 cursor-pointer">
          <input
            data-testid="context-snippets"
            type="checkbox"
            checked={contextCopyStore.snippets()}
            onChange={(e) => contextCopyStore.setSnippets(e.currentTarget.checked)}
          />
          <span class="text-neutral-300">Snippets</span>
        </label>

        <div>
          <div class="text-neutral-400 mb-1">Token budget</div>
          <div class="flex flex-wrap gap-1">
            <For each={TOKEN_BUDGETS}>
              {(b) => (
                <button
                  data-testid={`context-budget-${b}`}
                  class={`px-2 py-0.5 rounded ${contextCopyStore.maxTokens() === b ? "bg-indigo-600 text-white" : "bg-neutral-800 text-neutral-400 hover:text-white"}`}
                  onClick={() => contextCopyStore.setMaxTokens(b)}
                >
                  {b >= 1000 ? `${b / 1000}k` : b}
                </button>
              )}
            </For>
            <input
              data-testid="context-budget-custom"
              type="number"
              min="1"
              class="w-16 bg-neutral-800 rounded px-1 text-neutral-200"
              placeholder="custom"
              onChange={(e) => {
                const v = Number(e.currentTarget.value);
                if (v > 0) contextCopyStore.setMaxTokens(v);
              }}
            />
          </div>
        </div>

        <Show when={contextCopyStore.recent().length > 0}>
          <div>
            <div class="text-neutral-400 mb-1">Recent</div>
            <ul class="space-y-0.5">
              <For each={contextCopyStore.recent()}>
                {(b) => (
                  <li>
                    <button
                      data-testid="context-recent-item"
                      class="text-left text-indigo-300 hover:text-indigo-200 truncate block w-full"
                      title={b.label}
                      onClick={() => contextCopyStore.reopen(b)}
                    >
                      {b.label}
                    </button>
                  </li>
                )}
              </For>
            </ul>
          </div>
        </Show>
      </div>

      <div class="flex-1 overflow-y-auto p-2 min-w-0">
        <Show when={contextCopyStore.loading()}>
          <div class="text-neutral-400">Building context…</div>
        </Show>

        <Show when={err()}>
          {(message) => (
            <div data-testid="context-error" class="text-red-400 space-y-2">
              <div class="whitespace-pre-wrap">{message()}</div>
              <button
                data-testid="context-refresh-view"
                class="px-2 py-0.5 rounded bg-neutral-700 hover:bg-neutral-600 text-neutral-200"
                onClick={contextCopyStore.refreshView}
              >
                Refresh view
              </button>
            </div>
          )}
        </Show>

        <Show when={!contextCopyStore.loading() && !err() && result()}>
          {(r) => (
            <div class="space-y-2">
              <div class="flex items-center gap-2 flex-wrap">
                <span data-testid="context-token-estimate" class="text-neutral-400">
                  ~{r().tokens_estimate.toLocaleString()} tokens
                </span>
                <Show when={contextCopyStore.requestNote()}>
                  <span class="text-neutral-400">{contextCopyStore.requestNote()}</span>
                </Show>
                <button
                  data-testid="context-copy-clipboard"
                  class="ml-auto px-2 py-0.5 rounded bg-indigo-600 hover:bg-indigo-500 text-white"
                  onClick={contextCopyStore.copyToClipboard}
                >
                  Copy
                </button>
                <button
                  data-testid="context-download"
                  class="px-2 py-0.5 rounded bg-neutral-700 hover:bg-neutral-600 text-neutral-200"
                  onClick={contextCopyStore.downloadMarkdown}
                >
                  Download .md
                </button>
                <button
                  data-testid="context-raw-toggle"
                  class="px-2 py-0.5 rounded bg-neutral-800 hover:bg-neutral-700 text-neutral-300"
                  onClick={() => contextCopyStore.setRawView(!contextCopyStore.rawView())}
                >
                  {contextCopyStore.rawView() ? "Rendered" : "Raw"}
                </button>
              </div>

              <Show when={r().truncated}>
                <div data-testid="context-truncated-warning" class="text-amber-400">
                  ⚠ Truncated at {contextCopyStore.maxTokens().toLocaleString()} tokens.
                  <Show when={r().omitted.length > 0}>
                    {" "}Omitted: {r().omitted.join(", ")}
                  </Show>
                </div>
              </Show>

              <Show when={contextCopyStore.rawView()} fallback={<MarkdownPreview markdown={r().markdown} testId="context-preview-rendered" />}>
                <pre data-testid="context-preview-raw" class="text-[11px] text-neutral-300 whitespace-pre-wrap">{r().markdown}</pre>
              </Show>
            </div>
          )}
        </Show>

        <Show when={!contextCopyStore.loading() && !err() && !result()}>
          <div class="text-neutral-500">No context copied yet — use "Copy context" on a node, edge, flow, group, or scope.</div>
        </Show>
      </div>
    </div>
  );
}

// UF.6: the Unresolved tab (UN.0's ⚠ badge opens it, pre-filtered) gains
// kind filters + free-text search mirroring GET /api/unresolved's own
// params (service/kind/q) — the tree's badge count already fetches per
// service, but this tab is the one place that actually browses the ledger.
const UNRESOLVED_KINDS = ["import", "call", "route", "component"];

// unresolvedReason gives a human-readable one-liner for why a ref with a
// non-empty targets list was dropped rather than emitted as edges — the kind
// string alone (e.g. "dom_class_high_fanout") isn't self-explanatory. Falls
// back to a generic phrasing for any future kind that starts populating
// targets without a dedicated entry here.
function unresolvedReason(kind: string): string {
  switch (kind) {
    case "dom_class_high_fanout":
      return "Suppressed: this class name has more definition sites than the fan-out cap, so no edges were drawn to avoid noise.";
    default:
      return "Suppressed: candidates were dropped rather than drawn as edges.";
  }
}

function rowKey(ref: UnresolvedRef): string {
  return `${ref.service}\x00${ref.file}\x00${ref.line}\x00${ref.name}\x00${ref.kind}`;
}

function UnresolvedTab() {
  const filter = drawerStore.unresolvedFilter;
  const kindFilter = drawerStore.unresolvedKindFilter;
  const [service, setService] = createSignal("");
  const [kind, setKind] = createSignal("");
  const [q, setQ] = createSignal("");
  const [expanded, setExpanded] = createSignal<Set<string>>(new Set());

  function toggleExpanded(key: string, e: MouseEvent) {
    e.stopPropagation();
    const next = new Set(expanded());
    if (next.has(key)) next.delete(key);
    else next.add(key);
    setExpanded(next);
  }

  // A fresh "open pre-filtered to this file" request seeds service + a
  // free-text query for the file path (the server has no dedicated `file`
  // param — `q` already substring-matches File, see graph.FilterUnresolvedRefs).
  createEffect(() => {
    const f = filter();
    if (!f) return;
    setService(f.service);
    setQ(f.path);
  });

  // UO.3: Health dashboard's Unresolved card click-through seeds kind only.
  createEffect(() => {
    const k = kindFilter();
    if (!k) return;
    setKind(k);
  });

  const [result, { refetch }] = createResource(
    () => ({ service: service(), kind: kind(), q: q() }),
    async ({ service: svc, kind: k, q: query }) => {
      const p = new URLSearchParams();
      if (svc) p.set("service", svc);
      if (k) p.set("kind", k);
      if (query) p.set("q", query);
      p.set("limit", "200");
      const r = await apiFetch(`/api/unresolved?${p}`, { silent: true });
      return (await r.json()) as { refs: UnresolvedRef[]; total: number };
    },
  );

  function openFile(ref: UnresolvedRef) {
    scopeStore.push({ kind: "file", service: ref.service, path: ref.file });
  }

  return (
    <div data-testid="unresolved-tab" class="p-2 text-xs text-neutral-300 flex flex-col h-full gap-2">
      <Show when={filter()}>
        {(f) => (
          <span data-testid="unresolved-filter-chip" class="text-amber-400">
            ⚠ Unresolved · {f().service} · {f().path || "/"}
          </span>
        )}
      </Show>
      <div class="flex items-center gap-2 shrink-0">
        <input
          data-testid="unresolved-service"
          class="bg-neutral-800 rounded px-1.5 py-0.5 w-28"
          placeholder="service"
          value={service()}
          onInput={(e) => setService(e.currentTarget.value)}
        />
        <select
          data-testid="unresolved-kind"
          class="bg-neutral-800 rounded px-1.5 py-0.5"
          value={kind()}
          onChange={(e) => setKind(e.currentTarget.value)}
        >
          <option value="">all kinds</option>
          <For each={UNRESOLVED_KINDS}>{(k) => <option value={k}>{k}</option>}</For>
        </select>
        <input
          data-testid="unresolved-search"
          class="bg-neutral-800 rounded px-1.5 py-0.5 flex-1 min-w-0"
          placeholder="search name or file…"
          value={q()}
          onInput={(e) => setQ(e.currentTarget.value)}
        />
        <button class="text-neutral-400 hover:text-white shrink-0" onClick={refetch}>
          ↻
        </button>
      </div>
      <div class="flex-1 overflow-y-auto min-h-0">
        <Show when={result.loading}>
          <div class="text-neutral-400">Loading…</div>
        </Show>
        <Show when={!result.loading && result()}>
          {(r) => (
            <>
              <div class="text-neutral-400 mb-1">{r().total} unresolved ref{r().total === 1 ? "" : "s"}</div>
              <ul data-testid="unresolved-list" class="space-y-0.5">
                <For each={r().refs}>
                  {(ref) => {
                    const key = rowKey(ref);
                    const hasTargets = () => !!ref.targets;
                    const isOpen = () => expanded().has(key);
                    return (
                      <li>
                        <div
                          data-testid="unresolved-row"
                          class="flex items-center gap-2 px-1 py-0.5 rounded hover:bg-neutral-800 cursor-pointer"
                          onClick={() => openFile(ref)}
                        >
                          <Show when={hasTargets()}>
                            <button
                              data-testid="unresolved-expand-toggle"
                              class="text-neutral-500 hover:text-white shrink-0 w-3"
                              onClick={(e) => toggleExpanded(key, e)}
                            >
                              {isOpen() ? "▾" : "▸"}
                            </button>
                          </Show>
                          <span class="text-neutral-400 shrink-0">{ref.kind}</span>
                          <span class="text-neutral-200 truncate">{ref.name}</span>
                          <span class="text-neutral-500 truncate ml-auto">
                            {ref.file}:{ref.line}
                          </span>
                        </div>
                        <Show when={hasTargets() && isOpen()}>
                          <div
                            data-testid="unresolved-targets"
                            class="ml-5 mb-1 px-1.5 py-1 rounded bg-neutral-900 text-neutral-400"
                          >
                            <div class="text-amber-400/80 mb-0.5">{unresolvedReason(ref.kind)}</div>
                            <ul class="space-y-0.5">
                              <For each={ref.targets!.split("\n")}>
                                {(t) => (
                                  <li data-testid="unresolved-target-row" class="truncate">
                                    {t}
                                  </li>
                                )}
                              </For>
                            </ul>
                          </div>
                        </Show>
                      </li>
                    );
                  }}
                </For>
              </ul>
            </>
          )}
        </Show>
      </div>
    </div>
  );
}

// UnknownEdgeEntry mirrors internal/server/unknownedgesapi.go's
// unknownEdgeEntry (GET /api/unknown-edges).
interface UnknownEdgeEntry {
  confidence: string;
  type: string;
  from: string;
  from_id: string;
  service?: string;
  file?: string;
  line?: number;
  to: string;
  label?: string;
}

// The Unknown Edges tab: the web counterpart to `polyflow status
// --unknown-edges` and the MCP `unknown_edges` tool — same endpoint logic
// (contract.FilterEdgesByConfidence), so this list can't drift from what
// those report. Structured like UnresolvedTab (filter row + list), not
// like the canvas's confidence chip filter — that filter hides/dims edges
// already drawn from the current scope, this browses every low-confidence
// edge fleet-wide regardless of what's currently in view.
function UnknownEdgesTab() {
  const [minConfidence, setMinConfidence] = createSignal<string>("unknown");
  const [service, setService] = createSignal("");
  const [edgeType, setEdgeType] = createSignal("");

  const [result, { refetch }] = createResource(
    () => ({ minConfidence: minConfidence(), service: service(), edgeType: edgeType() }),
    async ({ minConfidence: mc, service: svc, edgeType: et }) => {
      const p = new URLSearchParams();
      p.set("min_confidence", mc);
      if (svc) p.set("service", svc);
      if (et) p.set("edge_type", et);
      p.set("limit", "200");
      const r = await apiFetch(`/api/unknown-edges?${p}`, { silent: true });
      return (await r.json()) as { edges: UnknownEdgeEntry[]; total: number; by_confidence: Record<string, number> };
    },
  );

  function openProducer(entry: UnknownEdgeEntry) {
    if (!entry.file) return;
    scopeStore.push({ kind: "file", service: entry.service ?? "", path: entry.file });
  }

  return (
    <div data-testid="unknown-edges-tab" class="p-2 text-xs text-neutral-300 flex flex-col h-full gap-2">
      <div class="flex items-center gap-2 shrink-0">
        <select
          data-testid="unknown-edges-min-confidence"
          class="bg-neutral-800 rounded px-1.5 py-0.5"
          value={minConfidence()}
          onChange={(e) => setMinConfidence(e.currentTarget.value)}
        >
          <For each={CONFIDENCE_LEVELS}>{(c) => <option value={c}>at or below {c}</option>}</For>
        </select>
        <input
          data-testid="unknown-edges-service"
          class="bg-neutral-800 rounded px-1.5 py-0.5 w-28"
          placeholder="service"
          value={service()}
          onInput={(e) => setService(e.currentTarget.value)}
        />
        <input
          data-testid="unknown-edges-type"
          class="bg-neutral-800 rounded px-1.5 py-0.5 w-28"
          placeholder="edge type"
          value={edgeType()}
          onInput={(e) => setEdgeType(e.currentTarget.value)}
        />
        <button class="text-neutral-400 hover:text-white shrink-0 ml-auto" onClick={refetch}>
          ↻
        </button>
      </div>
      <div class="flex-1 overflow-y-auto min-h-0">
        <Show when={result.loading}>
          <div class="text-neutral-400">Loading…</div>
        </Show>
        <Show when={!result.loading && result()}>
          {(r) => (
            <>
              <div class="text-neutral-400 mb-1">
                {r().total} edge{r().total === 1 ? "" : "s"}
                <Show when={Object.keys(r().by_confidence).length > 0}>
                  {" "}
                  ({Object.entries(r().by_confidence)
                    .map(([c, n]) => `${n} ${c}`)
                    .join(", ")})
                </Show>
              </div>
              <ul data-testid="unknown-edges-list" class="space-y-0.5">
                <For each={r().edges}>
                  {(entry) => (
                    <li
                      data-testid="unknown-edges-row"
                      class="flex items-center gap-2 px-1 py-0.5 rounded hover:bg-neutral-800 cursor-pointer"
                      onClick={() => openProducer(entry)}
                    >
                      <span class="text-amber-400 shrink-0">{entry.confidence}</span>
                      <span class="text-neutral-500 shrink-0">{entry.type}</span>
                      <span class="text-neutral-200 truncate">{entry.from}</span>
                      <span class="text-neutral-500 shrink-0">→</span>
                      <span class="text-neutral-300 truncate">{entry.to}</span>
                      <Show when={entry.file}>
                        <span class="text-neutral-500 truncate ml-auto">
                          {entry.file}:{entry.line}
                        </span>
                      </Show>
                    </li>
                  )}
                </For>
              </ul>
            </>
          )}
        </Show>
      </div>
    </div>
  );
}

export default function BottomDrawer() {
  const open = drawerStore.open;
  const setOpen = drawerStore.setOpen;

  // Drags from the handle strip above the header; dragging up (a smaller
  // clientY) should grow the drawer since it's anchored to the bottom edge,
  // so the height delta is startY - clientY rather than the other way round.
  function startResize(e: MouseEvent) {
    e.preventDefault();
    const startY = e.clientY;
    const startHeight = drawerStore.height();
    const onMove = (ev: MouseEvent) => {
      const next = Math.min(startHeight + (startY - ev.clientY), window.innerHeight - 80);
      drawerStore.setHeight(next);
    };
    const onUp = () => {
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
    };
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  }

  return (
    <div
      data-testid="bottom-drawer"
      class="shrink-0 border-t border-neutral-800 dark:border-neutral-700 bg-neutral-950 flex flex-col"
      classList={{ "transition-all": !open() }}
      style={{ height: open() ? `${drawerStore.height()}px` : "28px" }}
    >
      <Show when={open()}>
        <div
          data-testid="drawer-resize-handle"
          class="h-1 shrink-0 cursor-row-resize hover:bg-indigo-600/60 active:bg-indigo-600"
          onMouseDown={startResize}
        />
      </Show>
      <div class="flex items-center px-2 h-7 gap-2 text-xs text-neutral-400 shrink-0">
        <button onClick={() => setOpen(!open())} class="hover:text-white">
          {open() ? "▼" : "▲"} Drawer
        </button>
        <Show when={open()}>
          <For each={TABS}>
            {(tab) => (
              <button
                data-testid={`drawer-tab-${tab.id}`}
                class={drawerStore.activeTab() === tab.id ? "text-white" : "hover:text-white"}
                onClick={() => drawerStore.setActiveTab(tab.id)}
              >
                {tab.label}
              </button>
            )}
          </For>
        </Show>
        <Show when={open()}>
          <button onClick={() => setOpen(false)} class="ml-auto hover:text-white">× close</button>
        </Show>
      </div>
      <Show when={open()}>
        <div class="flex-1 overflow-hidden">
          <Show when={drawerStore.activeTab() === "context"}>
            <ContextTab />
          </Show>
          <Show when={drawerStore.activeTab() === "unresolved"}>
            <UnresolvedTab />
          </Show>
          <Show when={drawerStore.activeTab() === "unknown-edges"}>
            <UnknownEdgesTab />
          </Show>
          <Show when={drawerStore.activeTab() === "jobs"}>
            <JobsTab />
          </Show>
          <Show when={drawerStore.activeTab() === "toolcalls"}>
            <ToolCallsTab />
          </Show>
        </div>
      </Show>
    </div>
  );
}
