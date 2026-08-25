import { For, Show, createMemo } from "solid-js";
import { scopeStore } from "../../stores/scope";
import { registerCommand } from "../../commands/registry";
import { LENS_NAMES, DEFAULT_LENS, type LensName } from "./lenses";
import { EDGE_GROUP_NAMES } from "../../lib/edgeGroups";
import { effectiveAllOn } from "./FilterBar";
import Popover from "./Popover";

// Palette commands "Switch lens: <name>" (US.4) — registered once at module
// load, same pattern as commands/registry.ts's ACTIVITIES loop.
for (const name of LENS_NAMES) {
  registerCommand({
    id: `lens:${name}`,
    label: `Switch lens: ${name}`,
    run: () => scopeStore.setLens(name),
  });
}

// FilterBar's edgeType chips (lib/edgeGroups.ts) are a coarser, independent
// axis from the lens table. If the user has hand-edited those chips away
// from "all six groups on" while a lens is active, the rendered edge set no
// longer matches any pure lens — the control shows "Custom" rather than
// misreporting the active lens name.
export function displayedLens(filters: { edgeTypes: string[] }, lens: string | undefined): LensName | "Custom" {
  const eff = effectiveAllOn(filters.edgeTypes, EDGE_GROUP_NAMES);
  if (eff.length !== EDGE_GROUP_NAMES.length) return "Custom";
  return (lens as LensName) ?? DEFAULT_LENS;
}

export default function LensBar() {
  const active = createMemo(() => displayedLens(scopeStore.viewState().filters, scopeStore.viewState().lens));
  const lens = createMemo(() => scopeStore.viewState().lens ?? DEFAULT_LENS);

  return (
    <div data-testid="lens-bar" class="flex items-center text-xs">
      <Popover
        testId="lens-bar"
        label={`Lens: ${active()} ▾`}
        triggerClass="px-2 py-0.5 rounded text-xs border border-neutral-800 text-neutral-400 hover:text-neutral-300 transition-colors"
      >
        <div class="flex flex-col gap-1.5">
          <span class="text-neutral-500">lens</span>
          <div class="flex flex-wrap items-center gap-1">
            <For each={LENS_NAMES}>
              {(name) => (
                <button
                  class={`px-1.5 py-0.5 rounded transition-colors whitespace-nowrap ${
                    active() === name
                      ? "bg-neutral-700 text-white"
                      : "text-neutral-400 hover:text-white"
                  }`}
                  onClick={() => scopeStore.setLens(name)}
                >
                  {name}
                </button>
              )}
            </For>
            <Show when={active() === "Custom"}>
              <span class="px-1.5 py-0.5 rounded bg-neutral-800 text-neutral-300">Custom</span>
            </Show>
          </div>
        </div>
        <div class="h-px bg-neutral-800" />
        <div class="flex flex-wrap items-center gap-1">
          <button
            data-testid="lens-hide-unlinked"
            title="Hide nodes with no edge under this lens (default: dim to 30%)"
            class={`px-1.5 py-0.5 rounded border transition-colors whitespace-nowrap ${
              scopeStore.viewState().lensHideUnlinked
                ? "bg-neutral-700 text-white border-neutral-600"
                : "text-neutral-400 border-neutral-800 hover:text-neutral-300"
            }`}
            onClick={() => scopeStore.setLensHideUnlinked(!scopeStore.viewState().lensHideUnlinked)}
          >
            hide unlinked
          </button>
          <Show when={lens() === "Imports"}>
            <button
              data-testid="lens-rollup"
              title="Aggregate to file→file import edges with counts"
              class={`px-1.5 py-0.5 rounded border transition-colors whitespace-nowrap ${
                scopeStore.viewState().lensRollup
                  ? "bg-neutral-700 text-white border-neutral-600"
                  : "text-neutral-400 border-neutral-800 hover:text-neutral-300"
              }`}
              onClick={() => scopeStore.setLensRollup(!scopeStore.viewState().lensRollup)}
            >
              rollup
            </button>
          </Show>
        </div>
      </Popover>
    </div>
  );
}
