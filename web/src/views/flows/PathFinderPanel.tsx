// UF.2: path finder — "Find paths from A" panel. Triggered the same
// one-shot way UF.1's ThroughPanel is (pathFinderStore.requestedTo bridges
// the context-menu click to DetailHost, which mounts this for the target
// node). Fetches /api/flows/paths?from=&to=, ranks the result list for
// display (backend order is length-then-edge-id for determinism, not the
// "hops · confidence" reading order this panel wants), and supports
// per-path hover preview / click-isolate / "Overlay all".
import { createResource, createMemo, createSignal, For, Show } from "solid-js";
import { apiFetch, ApiError } from "../../lib/apiFetch";
import { scopeStore } from "../../stores/scope";
import { flowHighlightStore } from "../../stores/flowHighlight";
import { pathOverlayStore, OVERLAY_COLOR_COUNT } from "../../stores/pathOverlay";
import { drawerStore } from "../../stores/drawer";
import { parseChain, type FlowChain } from "../canvas/scopes/flow";
import { CONFIDENCE_LEVELS } from "../../lib/confidence";

interface RankedPath {
  index: number; // index into the raw backend `paths` array — FlowRef.index refers here
  chain: FlowChain;
  hops: number;
  worstConfidenceRank: number; // higher = worse (CONFIDENCE_LEVELS index); -1 = no edges
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
interface RawPathsResult {
  paths: { chain: any[] }[];
  reachable: boolean;
}

function worstConfidenceRank(chain: FlowChain): number {
  let worst = -1;
  for (const hop of chain.hops) {
    const c = hop.confidence || "static";
    const rank = Math.max(0, CONFIDENCE_LEVELS.indexOf(c as (typeof CONFIDENCE_LEVELS)[number]));
    if (rank > worst) worst = rank;
  }
  return worst;
}

function rankPaths(body: RawPathsResult): RankedPath[] {
  const ranked = body.paths.map((p, i) => {
    const chain = parseChain(p.chain);
    return {
      index: i,
      chain,
      hops: Math.max(0, chain.hops.length - 1),
      worstConfidenceRank: worstConfidenceRank(chain),
    };
  });
  ranked.sort((a, b) => a.hops - b.hops || a.worstConfidenceRank - b.worstConfidenceRank || a.index - b.index);
  return ranked;
}

interface NodeInfo {
  service: string;
  file: string;
}

async function fetchNodeInfo(id: string): Promise<NodeInfo | null> {
  try {
    const r = await apiFetch(`/api/node/${encodeURIComponent(id)}`, { silent: true });
    const body = (await r.json()) as { node?: { service?: string; file?: string } };
    if (!body.node) return null;
    return { service: body.node.service ?? "", file: body.node.file ?? "" };
  } catch {
    return null;
  }
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
async function fetchNearestEntrypoints(id: string): Promise<string[]> {
  try {
    const p = new URLSearchParams({ limit: "5" });
    const r = await apiFetch(`/api/flows/through/${encodeURIComponent(id)}?${p}`, { silent: true });
    const body = (await r.json()) as { flows: { entrypoint?: { label?: string } }[] };
    return [...new Set(body.flows.map((f) => f.entrypoint?.label).filter((l): l is string => !!l))];
  } catch {
    return [];
  }
}

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    try {
      const parsed = JSON.parse(err.body) as { error?: string };
      if (parsed.error) return parsed.error;
    } catch {
      // fall through to generic message
    }
  }
  return err instanceof Error ? err.message : String(err);
}

export default function PathFinderPanel(props: { from: string; fromLabel: string; to: string }) {
  const [overlayOn, setOverlayOn] = createSignal(false);

  const [resolution, { refetch }] = createResource(
    () => ({ from: props.from, to: props.to }),
    async ({ from, to }) => {
      const p = new URLSearchParams({ from, to, k: "20" });
      const r = await apiFetch(`/api/flows/paths?${p}`, { silent: true });
      return (await r.json()) as RawPathsResult;
    },
  );

  const ranked = createMemo(() => (resolution() ? rankPaths(resolution()!) : []));

  const [nearestFrom] = createResource(() => (resolution() && !resolution()!.reachable ? props.from : null), (id) => fetchNearestEntrypoints(id!));
  const [nearestTo] = createResource(() => (resolution() && !resolution()!.reachable ? props.to : null), (id) => fetchNearestEntrypoints(id!));

  function memberIds(chain: FlowChain): string[] {
    return chain.hops.map((h) => h.nodeId);
  }

  function isolate(p: RankedPath) {
    flowHighlightStore.clear();
    pathOverlayStore.clear();
    setOverlayOn(false);
    scopeStore.push({ kind: "flow", flow: { kind: "path", from: props.from, to: props.to, index: p.index } });
  }

  function toggleOverlay() {
    if (overlayOn()) {
      pathOverlayStore.clear();
      setOverlayOn(false);
      return;
    }
    pathOverlayStore.set(ranked().map((p) => memberIds(p.chain)));
    setOverlayOn(true);
  }

  async function openUnresolved(id: string) {
    const info = await fetchNodeInfo(id);
    if (info) drawerStore.openUnresolvedFor(info.service, info.file);
  }

  return (
    <div data-testid="path-finder-panel" class="mt-2 border-t border-neutral-800 pt-2">
      <div class="flex items-center justify-between gap-2 mb-1">
        <span class="text-xs text-neutral-400 truncate" title={`${props.fromLabel} → ${props.to}`}>
          Paths: {props.fromLabel} → {props.to}
        </span>
        <Show when={!resolution.loading && !resolution.error && ranked().length > 0}>
          <button
            data-testid="path-finder-overlay-toggle"
            class={`shrink-0 text-xs px-2 py-0.5 rounded border ${
              overlayOn() ? "border-indigo-400 text-indigo-300" : "border-neutral-700 text-neutral-400 hover:text-white"
            }`}
            onClick={toggleOverlay}
          >
            {overlayOn() ? "Overlaying all" : "Overlay all"}
          </button>
        </Show>
      </div>

      <Show when={resolution.loading}>
        <div class="text-xs text-neutral-400">Finding paths…</div>
      </Show>

      <Show when={resolution.error}>
        <div class="text-xs text-neutral-400 flex items-center gap-2">
          Failed to find paths.
          <button class="text-indigo-300 hover:text-indigo-200" onClick={refetch}>Retry</button>
        </div>
      </Show>

      <Show when={!resolution.loading && !resolution.error && resolution() && !resolution()!.reachable}>
        <div data-testid="path-finder-unreachable" class="text-xs text-neutral-400 space-y-2">
          <div>No static path {props.fromLabel} → {props.to}.</div>
          <div>
            <div class="text-neutral-400">Nearest entrypoints reaching A:</div>
            <Show when={nearestFrom() && nearestFrom()!.length > 0} fallback={<div class="text-neutral-500">none found</div>}>
              <ul class="list-disc list-inside">
                <For each={nearestFrom()}>{(label) => <li>{label}</li>}</For>
              </ul>
            </Show>
          </div>
          <div>
            <div class="text-neutral-400">Nearest entrypoints reaching B:</div>
            <Show when={nearestTo() && nearestTo()!.length > 0} fallback={<div class="text-neutral-500">none found</div>}>
              <ul class="list-disc list-inside">
                <For each={nearestTo()}>{(label) => <li>{label}</li>}</For>
              </ul>
            </Show>
          </div>
          <div class="flex gap-3">
            <button data-testid="path-finder-check-unresolved-from" class="text-indigo-300 hover:text-indigo-200" onClick={() => openUnresolved(props.from)}>
              check /api/unresolved for A
            </button>
            <button data-testid="path-finder-check-unresolved-to" class="text-indigo-300 hover:text-indigo-200" onClick={() => openUnresolved(props.to)}>
              check /api/unresolved for B
            </button>
          </div>
        </div>
      </Show>

      <ul class="space-y-1">
        <For each={ranked()}>
          {(p, i) => (
            <li
              data-testid="path-finder-row"
              class="px-2 py-1.5 rounded bg-neutral-900 hover:bg-neutral-800 cursor-pointer text-xs"
              onMouseEnter={() => flowHighlightStore.set(memberIds(p.chain))}
              onMouseLeave={() => flowHighlightStore.clear()}
              onClick={() => isolate(p)}
            >
              <div class="flex items-center justify-between gap-2">
                <span class="text-neutral-200">#{i() + 1}</span>
                <span class="text-neutral-400">
                  {p.hops} hop{p.hops === 1 ? "" : "s"} ·{" "}
                  {p.worstConfidenceRank < 0 ? "—" : CONFIDENCE_LEVELS[p.worstConfidenceRank]}
                </span>
              </div>
            </li>
          )}
        </For>
      </ul>
      <Show when={overlayOn()}>
        <div class="text-[10px] text-neutral-500 mt-1">
          {Math.min(ranked().length, OVERLAY_COLOR_COUNT)} colored, {Math.max(0, ranked().length - OVERLAY_COLOR_COUNT)} grouped
        </div>
      </Show>
    </div>
  );
}
