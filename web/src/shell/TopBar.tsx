import { createSignal, onMount, onCleanup, createEffect, Show, createMemo, For } from "solid-js";
import { layoutPrefs } from "../stores/layoutPrefs";
import { paletteStore } from "../stores/palette";
import Breadcrumbs from "./Breadcrumbs";
import GraphStats from "./GraphStats";
import LensBar from "../views/canvas/LensBar";
import { NO_CANVAS } from "../views/canvas/CanvasHost";
import { scopeStore, encodeViewState } from "../stores/scope";
import { pathFinderStore } from "../stores/pathFinder";
import { pinboardStore } from "../stores/pinboard";
import { selectionStore } from "../stores/selection";
import { displayLabel } from "../lib/location";
import { jobsStore } from "../stores/jobs";
import { captureStore } from "../stores/capture";
import { runtimeViewStore } from "../stores/runtimeView";
import { drawerStore } from "../stores/drawer";
import { savedViewsStore } from "../stores/savedViews";
import { canvasRefStore } from "../stores/canvasRef";
import { notificationsStore } from "../stores/notifications";
import {
  mermaidLevelForScope,
  mermaidTraceScopeFor,
  fetchMermaid,
  exportFilename,
  exportSVG,
  exportPNGBlob,
  exportElementsJSON,
  downloadText,
  downloadBlob,
} from "../lib/export";

export default function TopBar() {
  const [indexMenuOpen, setIndexMenuOpen] = createSignal(false);
  const [shareMenuOpen, setShareMenuOpen] = createSignal(false);
  const [saveDialogOpen, setSaveDialogOpen] = createSignal(false);
  const [saveNameInput, setSaveNameInput] = createSignal("");
  const [recordDialogOpen, setRecordDialogOpen] = createSignal(false);
  const [recordStopConfirm, setRecordStopConfirm] = createSignal(false);
  const [recordSessionInput, setRecordSessionInput] = createSignal("");
  const [recordHTTPPort, setRecordHTTPPort] = createSignal(4318);
  const [recordGRPCPort, setRecordGRPCPort] = createSignal(4317);
  const isCanvasPage = createMemo(() => !NO_CANVAS.has(scopeStore.stack().at(-1)?.kind ?? "search"));

  async function copyLink(): Promise<void> {
    const url = `${location.origin}${location.pathname}#v=${encodeViewState(scopeStore.viewState())}`;
    try {
      await navigator.clipboard?.writeText(url);
      notificationsStore.add({ id: `copy-link-${Date.now()}`, kind: "success", message: "Link copied to clipboard" });
    } catch {
      notificationsStore.add({ id: `copy-link-err-${Date.now()}`, kind: "error", message: "Could not copy link" });
    }
  }

  function currentScope() {
    return scopeStore.stack().at(-1);
  }

  async function exportMermaidCurrent(): Promise<void> {
    const scope = currentScope();
    if (!scope) return;
    const level = mermaidLevelForScope(scope);
    try {
      const text = await fetchMermaid(level, mermaidTraceScopeFor(scope));
      downloadText(exportFilename("mermaid", level), text, "text/plain");
    } catch {
      notificationsStore.add({ id: `export-mermaid-err-${Date.now()}`, kind: "error", message: "Mermaid export failed" });
    }
  }

  function exportSVGCurrent(): void {
    const cy = canvasRefStore.cy();
    if (!cy) return;
    try {
      downloadText(exportFilename("svg"), exportSVG(cy), "image/svg+xml");
    } catch {
      notificationsStore.add({ id: `export-svg-err-${Date.now()}`, kind: "error", message: "SVG export failed" });
    }
  }

  function exportPNGCurrent(): void {
    const cy = canvasRefStore.cy();
    if (!cy) return;
    try {
      downloadBlob(exportFilename("png"), exportPNGBlob(cy));
    } catch {
      // Canvas too large to rasterize even at the clamped scale — SVG has
      // no such size ceiling, so it's the fallback rather than a dead end.
      notificationsStore.add({
        id: `export-png-fallback-${Date.now()}`,
        kind: "info",
        message: "PNG export failed (canvas too large) — exported SVG instead",
      });
      exportSVGCurrent();
    }
  }

  function exportJSONCurrent(): void {
    const cy = canvasRefStore.cy();
    if (!cy) return;
    downloadText(exportFilename("json"), exportElementsJSON(cy), "application/json");
  }

  async function submitSaveView(): Promise<void> {
    const name = saveNameInput().trim();
    if (!name) return;
    const saved = await savedViewsStore.save(name);
    if (saved) {
      setSaveDialogOpen(false);
      setSaveNameInput("");
    }
  }

  function defaultSessionName(): string {
    return `ui-${new Date().toISOString().slice(0, 10)}`;
  }

  function openRecordDialog(): void {
    setRecordSessionInput(defaultSessionName());
    setRecordHTTPPort(4318);
    setRecordGRPCPort(4317);
    setRecordDialogOpen(true);
  }

  async function submitRecordStart(): Promise<void> {
    const name = recordSessionInput().trim() || defaultSessionName();
    const ok = await captureStore.start(name, recordHTTPPort(), recordGRPCPort());
    if (ok) setRecordDialogOpen(false);
  }

  async function confirmRecordStop(): Promise<void> {
    setRecordStopConfirm(false);
    const session = captureStore.activeSessions()[0]?.session;
    await captureStore.stop(session);
  }

  function recordTooltip(): string {
    const s = captureStore.activeSessions()[0];
    if (!s) return "Record runtime traffic";
    return `${s.spans_received} span${s.spans_received === 1 ? "" : "s"} · point OTEL_EXPORTER_OTLP_ENDPOINT at :${s.http_port}`;
  }

  function elapsedLabel(startedAt: string): string {
    const ms = Date.now() - Date.parse(startedAt);
    if (Number.isNaN(ms) || ms < 0) return "";
    const s = Math.floor(ms / 1000);
    return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${s % 60}s`;
  }

  function indexTooltip(): string {
    const job = jobsStore.activeIndexJob();
    if (!job) return "Index";
    const stage = job.log_tail?.[job.log_tail.length - 1];
    const parts = [`elapsed ${elapsedLabel(job.started_at)}`];
    if (stage) parts.push(stage);
    return parts.join(" · ");
  }

  // UO.6: light status polling so a CLI-started `polyflow capture start`
  // shows live in this tab too (mirrored, not just SSE-pushed for
  // sessions this tab itself started).
  onMount(() => {
    void captureStore.refreshStatus();
    captureStore.startPolling();
  });
  onCleanup(() => captureStore.stopPolling());

  // Stop/ingest completion prompt: "Captured N spans. Fuse into graph now?"
  createEffect(() => {
    const prompt = captureStore.fusePrompt();
    if (!prompt) return;
    notificationsStore.add({
      id: `capture-fuse-${prompt.session}-${Date.now()}`,
      kind: "info",
      message: `Captured ${prompt.spanCount} span${prompt.spanCount === 1 ? "" : "s"}. Fuse into graph now?`,
      action: {
        label: "Fuse now",
        onClick: () => {
          captureStore.fuseNow();
          runtimeViewStore.openRuntime(prompt.session);
        },
      },
    });
    captureStore.dismissFusePrompt();
  });

  createEffect(() => {
    if (layoutPrefs.theme() === "dark") {
      document.documentElement.classList.add("dark");
    } else {
      document.documentElement.classList.remove("dark");
    }
  });

  return (
    <header
      data-testid="top-bar"
      class="flex items-center gap-3 px-3 h-10 shrink-0 border-b border-neutral-800 dark:border-neutral-700 bg-neutral-950 text-sm"
    >
      <span class="font-semibold text-white">◆ polyflow</span>
      <Breadcrumbs />
      <Show when={isCanvasPage()}>
        <LensBar />
      </Show>
      {/* UF.7: pin tray — chips for pinboard pins, shown any time at least
          one pin exists (badges from 1; the canvas fade only starts at 2). */}
      <Show when={pinboardStore.pins().length > 0}>
        <div data-testid="pin-tray" class="flex items-center gap-1">
          <span class="text-neutral-400">pins:</span>
          <For each={pinboardStore.pins()}>
            {(p) => (
              <span
                data-testid="pin-chip"
                class="flex items-center gap-1 bg-neutral-800 border border-neutral-700 rounded px-2 py-0.5 text-xs text-neutral-300"
              >
                <button
                  class="truncate max-w-[160px] hover:text-white"
                  title={p.label}
                  onClick={() => selectionStore.setSelection({ kind: "node", id: p.id })}
                >
                  {displayLabel(p.label)}
                </button>
                <button class="text-neutral-400 hover:text-white" onClick={() => pinboardStore.unpin(p.id)}>
                  ×
                </button>
              </span>
            )}
          </For>
          <Show when={pinboardStore.active()}>
            <button
              data-testid="pin-tray-view-as-lane"
              class="text-xs text-indigo-300 hover:text-indigo-200"
              onClick={() => scopeStore.push({
                kind: "flow",
                flow: { kind: "pins", ids: pinboardStore.pins().map((p) => p.id) },
              })}
            >
              View as flow lane
            </button>
          </Show>
          <button
            data-testid="pin-tray-clear"
            class="text-xs text-neutral-400 hover:text-white"
            onClick={() => pinboardStore.clear()}
          >
            [clear all]
          </button>
        </div>
      </Show>
      <Show when={pathFinderStore.startNode()}>
        {(start) => (
          <span
            data-testid="path-start-chip"
            class="flex items-center gap-1 bg-neutral-800 border border-neutral-700 rounded px-2 py-0.5 text-xs text-neutral-300"
          >
            <span class="truncate max-w-[160px]" title={start().label}>A: {displayLabel(start().label)}</span>
            <button class="text-neutral-400 hover:text-white" onClick={() => pathFinderStore.clearStart()}>
              ×
            </button>
          </span>
        )}
      </Show>
      <div class="ml-auto flex items-center gap-2">
        <GraphStats />
        <div class="relative flex items-center">
          <Show
            when={captureStore.activeSessions().length > 0}
            fallback={
              <button
                data-testid="record-button"
                title="Record runtime traffic"
                class="flex items-center gap-1 text-xs text-neutral-400 hover:text-white border border-neutral-700 rounded px-2 py-0.5 disabled:opacity-40"
                disabled={captureStore.captureState() === "starting"}
                onClick={openRecordDialog}
              >
                <span class="inline-block w-2 h-2 rounded-full border border-neutral-500" />
                {captureStore.captureState() === "starting" ? "Starting…" : "Record"}
              </button>
            }
          >
            <button
              data-testid="record-active-button"
              title={recordTooltip()}
              class="flex items-center gap-1.5 text-xs text-red-300 hover:text-red-200 border border-red-800 rounded px-2 py-0.5 disabled:opacity-40"
              disabled={captureStore.captureState() === "stopping"}
              onClick={() => setRecordStopConfirm(true)}
            >
              <span data-testid="record-pulse" class="inline-block w-2.5 h-2.5 rounded-full bg-red-500 animate-pulse" />
              <span data-testid="record-span-count">{captureStore.activeSessions()[0]?.spans_received ?? 0}</span>
            </button>
          </Show>
          <Show when={recordStopConfirm()}>
            <div class="absolute top-full right-0 mt-1 z-20 bg-neutral-900 border border-neutral-700 rounded shadow-lg text-xs whitespace-nowrap p-2 flex items-center gap-2">
              <span class="text-neutral-300">Stop recording?</span>
              <button data-testid="record-stop-confirm" class="text-red-400 hover:text-red-300" onClick={() => void confirmRecordStop()}>
                Stop
              </button>
              <button class="text-neutral-400 hover:text-white" onClick={() => setRecordStopConfirm(false)}>
                Cancel
              </button>
            </div>
          </Show>
        </div>
        <div class="relative flex items-center">
          <Show
            when={jobsStore.activeIndexJob()}
            fallback={
              <>
                <button
                  data-testid="index-button"
                  class="text-xs text-neutral-400 hover:text-white border border-neutral-700 rounded-l px-2 py-0.5"
                  onClick={() => jobsStore.startIndex(false)}
                >
                  Index ▸
                </button>
                <button
                  data-testid="index-menu-toggle"
                  class="text-xs text-neutral-400 hover:text-white border border-l-0 border-neutral-700 rounded-r px-1 py-0.5"
                  onClick={() => setIndexMenuOpen((v) => !v)}
                >
                  ▾
                </button>
              </>
            }
          >
            {(job) => (
              <button
                data-testid="index-progress-button"
                title={indexTooltip()}
                class="flex items-center gap-1.5 text-xs text-indigo-300 hover:text-indigo-200 border border-indigo-800 rounded px-2 py-0.5"
                onClick={() => drawerStore.openJobs()}
              >
                <span class="inline-block w-2.5 h-2.5 rounded-full border-2 border-indigo-400 border-t-transparent animate-spin" />
                <span data-testid="index-progress-text">
                  {job().progress.done}/{job().progress.total || "?"}
                </span>
              </button>
            )}
          </Show>
          <Show when={indexMenuOpen()}>
            <div class="absolute top-full right-0 mt-1 z-20 bg-neutral-900 border border-neutral-700 rounded shadow-lg text-xs whitespace-nowrap">
              <button
                data-testid="index-full-reindex"
                class="block w-full text-left px-3 py-1.5 text-neutral-300 hover:bg-neutral-800 hover:text-white"
                onClick={() => {
                  setIndexMenuOpen(false);
                  jobsStore.startIndex(true);
                }}
              >
                Full re-index
              </button>
            </div>
          </Show>
        </div>
        <button
          data-testid="save-view-button"
          title="Save current view"
          class="text-xs text-neutral-400 hover:text-white border border-neutral-700 rounded px-2 py-0.5"
          onClick={() => {
            setSaveNameInput("");
            setSaveDialogOpen(true);
          }}
        >
          ☆
        </button>
        <div class="relative">
          <button
            data-testid="share-menu-toggle"
            class="text-xs text-neutral-400 hover:text-white border border-neutral-700 rounded px-2 py-0.5"
            onClick={() => setShareMenuOpen((v) => !v)}
          >
            Share ▾
          </button>
          <Show when={shareMenuOpen()}>
            <div class="absolute top-full right-0 mt-1 z-20 bg-neutral-900 border border-neutral-700 rounded shadow-lg text-xs whitespace-nowrap">
              <button
                data-testid="share-copy-link"
                class="block w-full text-left px-3 py-1.5 text-neutral-300 hover:bg-neutral-800 hover:text-white"
                onClick={() => {
                  setShareMenuOpen(false);
                  void copyLink();
                }}
              >
                Copy link
              </button>
              <button
                data-testid="share-export-png"
                disabled={!isCanvasPage() || !canvasRefStore.cy()}
                class="block w-full text-left px-3 py-1.5 text-neutral-300 hover:bg-neutral-800 hover:text-white disabled:opacity-40 disabled:hover:bg-transparent"
                onClick={() => {
                  setShareMenuOpen(false);
                  exportPNGCurrent();
                }}
              >
                Export PNG
              </button>
              <button
                data-testid="share-export-svg"
                disabled={!isCanvasPage() || !canvasRefStore.cy()}
                class="block w-full text-left px-3 py-1.5 text-neutral-300 hover:bg-neutral-800 hover:text-white disabled:opacity-40 disabled:hover:bg-transparent"
                onClick={() => {
                  setShareMenuOpen(false);
                  exportSVGCurrent();
                }}
              >
                Export SVG
              </button>
              <button
                data-testid="share-export-json"
                disabled={!isCanvasPage() || !canvasRefStore.cy()}
                class="block w-full text-left px-3 py-1.5 text-neutral-300 hover:bg-neutral-800 hover:text-white disabled:opacity-40 disabled:hover:bg-transparent"
                onClick={() => {
                  setShareMenuOpen(false);
                  exportJSONCurrent();
                }}
              >
                Export JSON
              </button>
              <button
                data-testid="share-export-mermaid"
                class="block w-full text-left px-3 py-1.5 text-neutral-300 hover:bg-neutral-800 hover:text-white"
                onClick={() => {
                  setShareMenuOpen(false);
                  void exportMermaidCurrent();
                }}
              >
                Export Mermaid
              </button>
            </div>
          </Show>
        </div>
        <button
          class="text-xs text-neutral-400 hover:text-white border border-neutral-700 rounded px-2 py-0.5"
          onClick={() => layoutPrefs.setTheme(layoutPrefs.theme() === "dark" ? "light" : "dark")}
        >
          {layoutPrefs.theme() === "dark" ? "☀" : "☾"}
        </button>
        <button
          class="text-xs text-neutral-400 hover:text-white border border-neutral-700 rounded px-2 py-0.5"
          onClick={() => paletteStore.open()}
        >
          ⌘K
        </button>
      </div>
      <Show when={recordDialogOpen()}>
        <div
          data-testid="record-dialog-overlay"
          class="fixed inset-0 z-50 flex items-start justify-center pt-24 bg-black/50"
          onClick={() => setRecordDialogOpen(false)}
        >
          <div
            class="w-full max-w-sm bg-neutral-900 border border-neutral-700 rounded-lg shadow-xl p-4 flex flex-col gap-3"
            onClick={(e) => e.stopPropagation()}
          >
            <span class="text-sm text-neutral-200">Record runtime traffic</span>
            <label class="text-xs text-neutral-400 flex flex-col gap-1">
              Session name
              <input
                data-testid="record-session-name-input"
                class="w-full px-2 py-1 bg-neutral-800 border border-neutral-700 rounded text-sm text-neutral-100 outline-none"
                value={recordSessionInput()}
                autofocus
                onInput={(e) => setRecordSessionInput(e.currentTarget.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") void submitRecordStart();
                  if (e.key === "Escape") setRecordDialogOpen(false);
                }}
              />
            </label>
            <div class="flex gap-3 text-xs text-neutral-400">
              <label class="flex flex-col gap-1">
                HTTP port
                <input
                  data-testid="record-http-port-input"
                  type="number"
                  class="w-20 px-2 py-1 bg-neutral-800 border border-neutral-700 rounded text-sm text-neutral-100 outline-none"
                  value={recordHTTPPort()}
                  onInput={(e) => setRecordHTTPPort(parseInt(e.currentTarget.value, 10) || 4318)}
                />
              </label>
              <label class="flex flex-col gap-1">
                gRPC port
                <input
                  data-testid="record-grpc-port-input"
                  type="number"
                  class="w-20 px-2 py-1 bg-neutral-800 border border-neutral-700 rounded text-sm text-neutral-100 outline-none"
                  value={recordGRPCPort()}
                  onInput={(e) => setRecordGRPCPort(parseInt(e.currentTarget.value, 10) || 4317)}
                />
              </label>
            </div>
            <div class="flex justify-end gap-2 text-xs">
              <button class="px-2 py-1 text-neutral-400 hover:text-white" onClick={() => setRecordDialogOpen(false)}>
                Cancel
              </button>
              <button
                data-testid="record-dialog-submit"
                class="px-2 py-1 bg-indigo-700 hover:bg-indigo-600 text-white rounded disabled:opacity-40"
                disabled={captureStore.captureState() === "starting"}
                onClick={() => void submitRecordStart()}
              >
                Start
              </button>
            </div>
          </div>
        </div>
      </Show>
      <Show when={saveDialogOpen()}>
        <div
          data-testid="save-view-overlay"
          class="fixed inset-0 z-50 flex items-start justify-center pt-24 bg-black/50"
          onClick={() => setSaveDialogOpen(false)}
        >
          <div
            class="w-full max-w-sm bg-neutral-900 border border-neutral-700 rounded-lg shadow-xl p-4 flex flex-col gap-3"
            onClick={(e) => e.stopPropagation()}
          >
            <span class="text-sm text-neutral-200">Save current view</span>
            <input
              data-testid="save-view-name-input"
              class="w-full px-2 py-1 bg-neutral-800 border border-neutral-700 rounded text-sm text-neutral-100 outline-none"
              placeholder="e.g. fleet rabbitmq seam"
              value={saveNameInput()}
              autofocus
              onInput={(e) => setSaveNameInput(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void submitSaveView();
                if (e.key === "Escape") setSaveDialogOpen(false);
              }}
            />
            <div class="flex justify-end gap-2 text-xs">
              <button
                class="px-2 py-1 text-neutral-400 hover:text-white"
                onClick={() => setSaveDialogOpen(false)}
              >
                Cancel
              </button>
              <button
                data-testid="save-view-submit"
                class="px-2 py-1 bg-indigo-700 hover:bg-indigo-600 text-white rounded disabled:opacity-40"
                disabled={!saveNameInput().trim()}
                onClick={() => void submitSaveView()}
              >
                Save
              </button>
            </div>
          </div>
        </div>
      </Show>
    </header>
  );
}
