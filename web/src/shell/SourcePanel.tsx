import { createEffect, createSignal, For, Show, untrack } from "solid-js";
import { apiFetchJSON } from "../lib/apiFetch";
import { notificationsStore } from "../stores/notifications";
import { formatLocation } from "../lib/location";

// Bounded source view for the detail panel (UN.3): fetches
// GET /api/node/{id}/source?range=1&context=<n>, highlights the node's own
// extent, dims the surrounding context, and lets the user widen the window
// or fall back to the whole file. No syntax-highlight tokenizer is wired —
// the repo has no highlighter dependency today and the plan asks not to add
// a heavy one for this; plain monospace with line numbers is the tradeoff.

interface RangeSource {
  file: string;
  start: number;
  end: number;
  context: number;
  first_line: number;
  lines: string[];
}

const DEFAULT_CONTEXT = 5;
const CONTEXT_STEP = 10;

export default function SourcePanel(props: { nodeId: string }) {
  const [data, setData] = createSignal<RangeSource | null>(null);
  const [context, setContext] = createSignal(DEFAULT_CONTEXT);
  const [wholeFile, setWholeFile] = createSignal(false);
  const [loading, setLoading] = createSignal(false);
  const [error, setError] = createSignal<string | undefined>(undefined);

  async function load(): Promise<void> {
    const id = props.nodeId;
    setLoading(true);
    setError(undefined);
    try {
      if (wholeFile()) {
        const res = await apiFetchJSON<{ source: string }>(`/api/node/${encodeURIComponent(id)}/source`);
        const prev = data();
        setData({
          file: prev?.file ?? "",
          start: prev?.start ?? 0,
          end: prev?.end ?? 0,
          context: context(),
          first_line: 1,
          lines: res.source.split("\n"),
        });
      } else {
        const params = new URLSearchParams({ range: "1", context: String(context()) });
        const res = await apiFetchJSON<RangeSource>(`/api/node/${encodeURIComponent(id)}/source?${params}`);
        setData(res);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  // `load()` reads `wholeFile`/`context`/`data` synchronously before its
  // first await; without `untrack` those reads register as dependencies of
  // *this* effect (it's still the active tracking scope at that point), so
  // a later `setWholeFile`/`setContext` from the toolbar buttons would
  // re-trigger this mount effect and immediately stomp the change back.
  createEffect(() => {
    const id = props.nodeId; // the only intentional dependency
    untrack(() => {
      void id;
      setContext(DEFAULT_CONTEXT);
      setWholeFile(false);
      void load();
    });
  });

  function expandContext(): void {
    setContext((c) => c + CONTEXT_STEP);
    void load();
  }

  function toggleWholeFile(): void {
    setWholeFile((w) => !w);
    void load();
  }

  function copyPath(): void {
    const d = data();
    if (!d) return;
    const loc = formatLocation(d.file, d.start, d.end);
    navigator.clipboard?.writeText(loc).catch(() => {});
    notificationsStore.add({ id: `copy-source-${Date.now()}`, kind: "info", message: `Copied: ${loc}` });
  }

  return (
    <div data-testid="source-panel" class="mt-3 border-t border-neutral-800 pt-2">
      <Show when={loading() && !data()}>
        <div class="text-xs text-neutral-400">Loading source…</div>
      </Show>
      <Show when={error()}>
        <div class="text-xs text-red-400" data-testid="source-error">{error()}</div>
      </Show>
      <Show when={data()}>
        {(d) => (
          <div>
            <div class="flex items-center justify-between mb-1 gap-2">
              <span class="text-[11px] text-neutral-400 truncate" data-testid="source-location">
                {formatLocation(d().file, d().start, d().end) || d().file}
              </span>
              <div class="flex gap-2 shrink-0">
                <button class="text-[11px] text-blue-400 hover:text-blue-300" onClick={copyPath}>
                  copy path
                </button>
                <Show when={!wholeFile() && d().end !== 0}>
                  <button
                    data-testid="source-expand-context"
                    class="text-[11px] text-blue-400 hover:text-blue-300"
                    onClick={expandContext}
                  >
                    +{CONTEXT_STEP} lines
                  </button>
                </Show>
                <button
                  data-testid="source-toggle-whole-file"
                  class="text-[11px] text-blue-400 hover:text-blue-300"
                  onClick={toggleWholeFile}
                >
                  {wholeFile() ? "bounded" : "whole file"}
                </button>
              </div>
            </div>
            <pre data-testid="source-code" class="text-xs font-mono bg-neutral-900 rounded p-1">
              <For each={d().lines}>
                {(line, i) => {
                  const lineNo = d().first_line + i();
                  const inExtent = !wholeFile() && d().end !== 0 && lineNo >= d().start && lineNo <= d().end;
                  return (
                    <div
                      data-testid="source-line"
                      data-line={lineNo}
                      data-highlighted={inExtent ? "true" : "false"}
                      class={`px-1 whitespace-pre-wrap break-all ${inExtent ? "bg-blue-950/60 text-neutral-100" : "text-neutral-400"}`}
                    >
                      <span class="inline-block w-10 text-right pr-2 text-neutral-500 select-none">{lineNo}</span>
                      {line}
                    </div>
                  );
                }}
              </For>
            </pre>
          </div>
        )}
      </Show>
    </div>
  );
}
