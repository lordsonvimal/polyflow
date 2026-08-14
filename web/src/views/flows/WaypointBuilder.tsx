// UF.2: waypoint flow builder — "Start flow here" seeds a session
// (waypointBuilderStore), this panel lets the user click upstream/
// downstream candidates to grow the chain, and the canvas (FlowLane, via
// scopeStore) shows the growing lane live after every change. Chips are
// removable mid-list; a broken remainder surfaces the backend's
// disconnected-pair error inline without discarding the edit.
import { createResource, createMemo, createEffect, untrack, For, Show } from "solid-js";
import { apiFetch, ApiError } from "../../lib/apiFetch";
import { scopeStore, Scope } from "../../stores/scope";
import { waypointBuilderStore, type WaypointRef } from "../../stores/waypointBuilder";
import { displayLabel } from "../../lib/location";

interface CandidateNode {
  node_id: string;
  label: string;
  service: string;
  type: string;
  via_edge_type: string;
}

interface RawRefineResult {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  chain: any[];
  candidates: { upstream: CandidateNode[]; downstream: CandidateNode[] };
}

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    try {
      const parsed = JSON.parse(err.body) as { error?: string };
      if (parsed.error) return parsed.error;
    } catch {
      // fall through
    }
  }
  return err instanceof Error ? err.message : String(err);
}

export default function WaypointBuilder() {
  const ids = createMemo(() => waypointBuilderStore.waypoints().map((w) => w.id));
  const direction = waypointBuilderStore.direction;

  const [refine, { refetch }] = createResource(
    () => (ids().length > 0 ? { ids: ids(), direction: direction() } : null),
    async (args) => {
      const p = new URLSearchParams({ waypoints: args!.ids.join(","), direction: args!.direction });
      const r = await apiFetch(`/api/flows/refine?${p}`, { silent: true });
      return (await r.json()) as RawRefineResult;
    },
  );

  // Live lane: push the flow scope once this session starts producing
  // waypoints, then swap it in place (replaceTop) for every later change so
  // the stack never grows per click — scopeStore.push per keystroke would
  // wreck breadcrumb/Esc navigation (UF.2 outcome note, mirrors UF.0's
  // "never guess a request shape" discipline for the honest-empty case).
  // Plain (non-reactive) bookkeeping, not a signal: it only needs to
  // persist across effect runs, not trigger them.
  let pushed = false;
  createEffect(() => {
    const currentIds = ids();
    const d = direction();
    // scopeStore's own methods read-then-write its signal; calling them
    // untracked keeps this effect's dependencies to exactly ids()/
    // direction() — without it, Solid attributes the read inside
    // e.g. replaceTop to *this* effect too, and the write it performs
    // re-triggers the same effect every run, forever.
    untrack(() => {
      if (currentIds.length === 0) {
        if (pushed) {
          const stack = scopeStore.stack();
          scopeStore.popTo(Math.max(0, stack.length - 2));
          pushed = false;
        }
        return;
      }
      const flowScope: Scope = { kind: "flow", flow: { kind: "waypoints", ids: currentIds, direction: d } };
      if (pushed) {
        scopeStore.replaceTop(flowScope);
      } else {
        scopeStore.push(flowScope);
        pushed = true;
      }
    });
  });

  function toRef(c: CandidateNode): WaypointRef {
    return { id: c.node_id, label: c.label };
  }

  function clear() {
    waypointBuilderStore.clear();
  }

  return (
    <div data-testid="waypoint-builder" class="p-2 text-xs">
      <div class="flex items-center justify-between mb-2">
        <span class="text-neutral-400 font-medium">Waypoint flow builder</span>
        <button data-testid="waypoint-clear" class="text-neutral-400 hover:text-white" onClick={clear}>
          clear
        </button>
      </div>

      <div class="flex flex-wrap gap-1 mb-2">
        <For each={waypointBuilderStore.waypoints()}>
          {(w, i) => (
            <span
              data-testid="waypoint-chip"
              class="flex items-center gap-1 bg-neutral-800 border border-neutral-700 rounded px-2 py-0.5 text-neutral-200"
            >
              <span class="truncate max-w-[120px]" title={w.label}>{displayLabel(w.label)}</span>
              <button
                data-testid="waypoint-chip-remove"
                class="text-neutral-400 hover:text-white"
                onClick={() => waypointBuilderStore.removeAt(i())}
              >
                ×
              </button>
            </span>
          )}
        </For>
      </div>

      <Show when={waypointBuilderStore.waypoints().length === 0}>
        <div class="text-neutral-500">"Start flow here" from the canvas context menu to seed a session.</div>
      </Show>

      <Show when={refine.loading}>
        <div class="text-neutral-400">Refining…</div>
      </Show>

      <Show when={refine.error}>
        <div data-testid="waypoint-error" class="text-red-400 mb-2">
          {errorMessage(refine.error)}
          <button class="ml-2 text-indigo-300 hover:text-indigo-200" onClick={refetch}>Retry</button>
        </div>
      </Show>

      <Show when={!refine.error && refine()}>
        {(res) => (
          <div class="space-y-2">
            <div>
              <div class="text-neutral-400 mb-1">Upstream</div>
              <Show when={res().candidates.upstream.length > 0} fallback={<div class="text-neutral-700">none</div>}>
                <ul class="space-y-1">
                  <For each={res().candidates.upstream}>
                    {(c) => (
                      <li
                        data-testid="waypoint-candidate-upstream"
                        class="px-2 py-1 rounded bg-neutral-900 hover:bg-neutral-800 cursor-pointer"
                        onClick={() => waypointBuilderStore.prepend(toRef(c))}
                      >
                        {displayLabel(c.label)} <span class="text-neutral-500">· {c.via_edge_type}</span>
                      </li>
                    )}
                  </For>
                </ul>
              </Show>
            </div>
            <div>
              <div class="text-neutral-400 mb-1">Downstream</div>
              <Show when={res().candidates.downstream.length > 0} fallback={<div class="text-neutral-700">none</div>}>
                <ul class="space-y-1">
                  <For each={res().candidates.downstream}>
                    {(c) => (
                      <li
                        data-testid="waypoint-candidate-downstream"
                        class="px-2 py-1 rounded bg-neutral-900 hover:bg-neutral-800 cursor-pointer"
                        onClick={() => waypointBuilderStore.append(toRef(c))}
                      >
                        {displayLabel(c.label)} <span class="text-neutral-500">· {c.via_edge_type}</span>
                      </li>
                    )}
                  </For>
                </ul>
              </Show>
            </div>
          </div>
        )}
      </Show>
    </div>
  );
}
