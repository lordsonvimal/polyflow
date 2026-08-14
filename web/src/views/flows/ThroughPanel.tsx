// UF.1: the "Isolate flows through here" catalog entry — detail-panel
// section triggered from the node's panel button or the canvas context
// menu. Lists /api/flows/through/{id} grouped by entrypoint (one row per
// entrypoint, even when several of its chains pass through the target);
// hovering a row cheaply pre-highlights that group's member nodes on the
// current canvas (flowHighlightStore — classes only, no layout call);
// clicking isolates the group as a UF.0 flow lane.
import { createResource, createMemo, For, Show } from "solid-js";
import { apiFetch } from "../../lib/apiFetch";
import { scopeStore } from "../../stores/scope";
import { flowHighlightStore } from "../../stores/flowHighlight";
import { parseChain, minVerificationState, type FlowChain } from "../canvas/scopes/flow";
import { displayLabel } from "../../lib/location";

interface ThroughGroup {
  entrypointId: string;
  label: string;
  kind: string;
  chains: FlowChain[];
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
interface RawFlowsThrough {
  flows: { entrypoint: any; chain: any[] }[];
  truncated: boolean;
}

function groupByEntrypoint(body: RawFlowsThrough): ThroughGroup[] {
  const byId = new Map<string, ThroughGroup>();
  for (const f of body.flows) {
    const id = f.entrypoint?.node_id as string | undefined;
    if (!id) continue;
    const chain = parseChain(f.chain);
    const existing = byId.get(id);
    if (existing) {
      existing.chains.push(chain);
    } else {
      byId.set(id, {
        entrypointId: id,
        label: (f.entrypoint?.label as string) ?? id,
        kind: (f.entrypoint?.kind as string) ?? "",
        chains: [chain],
      });
    }
  }
  return [...byId.values()].sort((a, b) => a.label.localeCompare(b.label));
}

function servicesTouched(chains: FlowChain[]): string[] {
  return [...new Set(chains.flatMap((c) => c.hops.map((h) => h.service)).filter(Boolean))].sort();
}

function maxHopCount(chains: FlowChain[]): number {
  return chains.reduce((max, c) => Math.max(max, c.hops.length), 0);
}

const VERIFICATION_BADGE: Record<string, { label: string; class: string }> = {
  verified: { label: "verified", class: "text-emerald-400" },
  candidate: { label: "candidate", class: "text-amber-400" },
  conflicting: { label: "conflicting", class: "text-red-400" },
  observed_only_gap: { label: "observed only", class: "text-red-400" },
};

export default function ThroughPanel(props: { nodeId: string }) {
  const [resolution] = createResource(
    () => props.nodeId,
    async (id) => {
      const p = new URLSearchParams({ limit: "20" });
      const r = await apiFetch(`/api/flows/through/${encodeURIComponent(id)}?${p}`, { silent: true });
      return (await r.json()) as RawFlowsThrough;
    },
  );

  const groups = createMemo(() => (resolution() ? groupByEntrypoint(resolution()!) : []));

  function memberIds(group: ThroughGroup): string[] {
    const ids = new Set<string>();
    for (const chain of group.chains) {
      for (const hop of chain.hops) ids.add(hop.nodeId);
    }
    return [...ids];
  }

  function isolate(group: ThroughGroup) {
    flowHighlightStore.clear();
    scopeStore.push({ kind: "flow", flow: { kind: "through", nodeId: props.nodeId, entrypointId: group.entrypointId } });
  }

  return (
    <div data-testid="through-panel" class="mt-2 border-t border-neutral-800 pt-2">
      <Show when={resolution.loading}>
        <div class="text-xs text-neutral-500">Loading flows…</div>
      </Show>
      <Show when={resolution.error}>
        <div class="text-xs text-neutral-500">Failed to load flows through here.</div>
      </Show>
      <Show when={!resolution.loading && !resolution.error && groups().length === 0}>
        <div class="text-xs text-neutral-500" data-testid="through-panel-empty">
          No flows pass through this node.
        </div>
      </Show>
      <ul class="space-y-1">
        <For each={groups()}>
          {(group) => {
            const min = createMemo(() => minVerificationState(group.chains));
            const badge = () => VERIFICATION_BADGE[min()] ?? VERIFICATION_BADGE.verified;
            return (
              <li
                data-testid="through-panel-row"
                class="px-2 py-1.5 rounded bg-neutral-900 hover:bg-neutral-800 cursor-pointer text-xs"
                onMouseEnter={() => flowHighlightStore.set(memberIds(group))}
                onMouseLeave={() => flowHighlightStore.clear()}
                onClick={() => isolate(group)}
              >
                <div class="flex items-center justify-between gap-2">
                  <span class="text-neutral-200 truncate" title={group.label}>{displayLabel(group.label)}</span>
                  <span class={`shrink-0 ${badge().class}`}>{badge().label}</span>
                </div>
                <div class="text-neutral-500 mt-0.5">
                  {maxHopCount(group.chains)} hop{maxHopCount(group.chains) === 1 ? "" : "s"} ·{" "}
                  {servicesTouched(group.chains).join(", ") || "—"}
                </div>
              </li>
            );
          }}
        </For>
      </ul>
      <Show when={resolution()?.truncated}>
        <div class="text-[10px] text-neutral-600 mt-1">more entrypoints exist past the fetch limit</div>
      </Show>
    </div>
  );
}
