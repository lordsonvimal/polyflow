import { For, Show, createMemo, onMount } from "solid-js";
import { scopeStore, type ViewState } from "../../stores/scope";
import { treeStore } from "../../stores/tree";
import { CONFIDENCE_LEVELS, DEFAULT_CONFIDENCE } from "../../lib/confidence";
import { EDGE_GROUP_NAMES } from "../../lib/edgeGroups";

type Filters = ViewState["filters"];

const OPT_IN_CONFIDENCE = CONFIDENCE_LEVELS.filter((c) => !DEFAULT_CONFIDENCE.includes(c));

// The set actually in effect for a "default = everything on" filter axis
// (edge-type groups, services): [] means unrestricted, so every name is
// active until the user turns one off.
export function effectiveAllOn(explicit: readonly string[], allNames: readonly string[]): string[] {
  return explicit.length > 0 ? [...explicit] : [...allNames];
}

// The set in effect for confidence, which defaults to DEFAULT_CONFIDENCE
// rather than "all tiers" (partial/unknown are opt-in).
export function effectiveConfidence(explicit: readonly string[]): string[] {
  return explicit.length > 0 ? [...explicit] : [...DEFAULT_CONFIDENCE];
}

function toggled(effective: readonly string[], all: readonly string[], name: string): string[] {
  const next = effective.includes(name) ? effective.filter((x) => x !== name) : [...effective, name];
  // Canonicalize "everything active" back to [] so the URL-encoded state
  // stays minimal and a freshly-loaded workspace (all-on) round-trips clean.
  if (next.length === all.length && all.every((n) => next.includes(n))) return [];
  return next;
}

// Pure — number of chips deviating from the fully-open default state, for
// the active-count badge.
export function computeActiveCount(filters: Filters, allServices: readonly string[]): number {
  let count = 0;
  if (filters.confidence.length > 0) {
    const eff = effectiveConfidence(filters.confidence);
    for (const c of CONFIDENCE_LEVELS) {
      const onByDefault = DEFAULT_CONFIDENCE.includes(c);
      const onNow = eff.includes(c);
      if (onByDefault !== onNow) count++;
    }
  }
  if (filters.edgeTypes.length > 0) {
    count += EDGE_GROUP_NAMES.length - effectiveAllOn(filters.edgeTypes, EDGE_GROUP_NAMES).length;
  }
  if (filters.services.length > 0) {
    count += allServices.length - effectiveAllOn(filters.services, allServices).length;
  }
  return count;
}

function Chip(props: { label: string; active: boolean; dashed?: boolean; onClick: () => void }) {
  return (
    <button
      class={`px-2 py-0.5 rounded text-xs border transition-colors ${
        props.active
          ? "bg-neutral-700 text-white border-neutral-600"
          : "bg-transparent text-neutral-500 border-neutral-800 hover:text-neutral-300"
      } ${props.dashed ? "border-dashed" : ""}`}
      onClick={props.onClick}
    >
      {props.label}
    </button>
  );
}

export default function FilterBar() {
  onMount(() => treeStore.loadServices());
  const filters = () => scopeStore.viewState().filters;
  const services = createMemo(() => treeStore.services().map((s) => s.name));
  const activeCount = createMemo(() => computeActiveCount(filters(), services()));

  function toggleConfidence(tier: string) {
    const eff = effectiveConfidence(filters().confidence);
    const next = eff.includes(tier) ? eff.filter((x) => x !== tier) : [...eff, tier];
    scopeStore.setFilters({ ...filters(), confidence: next });
  }

  function toggleEdgeGroup(group: string) {
    const eff = effectiveAllOn(filters().edgeTypes, EDGE_GROUP_NAMES);
    scopeStore.setFilters({ ...filters(), edgeTypes: toggled(eff, EDGE_GROUP_NAMES, group) });
  }

  function toggleService(service: string) {
    const all = services();
    const eff = effectiveAllOn(filters().services, all);
    scopeStore.setFilters({ ...filters(), services: toggled(eff, all, service) });
  }

  function reset() {
    scopeStore.setFilters({ confidence: [], edgeTypes: [], services: [] });
  }

  return (
    <div
      data-testid="filter-bar"
      class="flex items-center gap-3 px-2 py-1 border-b border-neutral-800 bg-neutral-900 text-xs overflow-x-auto shrink-0"
    >
      <div class="flex items-center gap-1">
        <For each={CONFIDENCE_LEVELS}>
          {(tier) => (
            <Chip
              label={tier}
              active={effectiveConfidence(filters().confidence).includes(tier)}
              dashed={OPT_IN_CONFIDENCE.includes(tier)}
              onClick={() => toggleConfidence(tier)}
            />
          )}
        </For>
      </div>
      <div class="w-px h-4 bg-neutral-800" />
      <div class="flex items-center gap-1">
        <For each={EDGE_GROUP_NAMES}>
          {(group) => (
            <Chip
              label={group}
              active={effectiveAllOn(filters().edgeTypes, EDGE_GROUP_NAMES).includes(group)}
              onClick={() => toggleEdgeGroup(group)}
            />
          )}
        </For>
      </div>
      <Show when={services().length > 0}>
        <div class="w-px h-4 bg-neutral-800" />
        <div class="flex items-center gap-1">
          <For each={services()}>
            {(svc) => (
              <Chip
                label={svc}
                active={effectiveAllOn(filters().services, services()).includes(svc)}
                onClick={() => toggleService(svc)}
              />
            )}
          </For>
        </div>
      </Show>
      <div class="ml-auto flex items-center gap-2">
        <Show when={activeCount() > 0}>
          <span
            data-testid="filter-active-count"
            class="px-1.5 py-0.5 rounded-full bg-indigo-600 text-white text-[10px] leading-none"
          >
            {activeCount()}
          </span>
          <button class="text-neutral-500 hover:text-white" onClick={reset}>
            reset
          </button>
        </Show>
      </div>
    </div>
  );
}
