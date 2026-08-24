import { For, Show, onMount, onCleanup } from "solid-js";
import { fleetMembersStore } from "../../stores/fleetMembers";

// GR.6 (revised): every locally-resolved fleet member is merged into one
// fleet-wide view by default (browsing/search/impact/context/trace all see
// the whole fleet at once, GR.3's federation) — there's no single "active"
// member to switch between anymore. This panel lists every member with its
// resolved/unresolved status, next to FleetStatusPanel's per-member
// breakdown table; a "Load" button on an unresolved one triggers GR.1's
// resolve-or-clone and widens the merge to include it. Renders nothing when
// this workspace isn't a registered Tier-GR fleet member (an empty services
// list), same "bonus, not a requirement" contract the backend follows.
export default function FleetSwitcher() {
  onMount(() => {
    fleetMembersStore.load();
    const unsubscribe = fleetMembersStore.subscribe();
    onCleanup(unsubscribe);
  });

  return (
    <Show when={fleetMembersStore.services().length > 0}>
      <div data-testid="fleet-switcher" class="p-2 text-xs">
        <div class="text-neutral-400 mb-1">Fleet members (all resolved ones are merged into this view)</div>
        <ul class="flex flex-col gap-1">
          <For each={fleetMembersStore.services()}>
            {(s) => (
              <li data-testid="fleet-switcher-row" class="flex items-center gap-2">
                <span class={s.active ? "text-green-400" : "text-neutral-500"}>{s.active ? "●" : "○"}</span>
                <span class="text-neutral-200 font-mono">{s.service}</span>
                <Show
                  when={!s.active}
                  fallback={
                    <span class="text-neutral-500" data-testid="fleet-switcher-loaded">
                      loaded
                    </span>
                  }
                >
                  <button
                    data-testid="fleet-switcher-load"
                    class="px-2 py-0.5 rounded bg-neutral-700 hover:bg-neutral-600 text-neutral-200 disabled:opacity-50"
                    disabled={fleetMembersStore.switching()}
                    onClick={() => fleetMembersStore.setActive(s.service)}
                  >
                    Load
                  </button>
                </Show>
              </li>
            )}
          </For>
        </ul>
        <Show when={fleetMembersStore.switching()}>
          <div data-testid="fleet-switcher-loading" class="text-neutral-500 mt-1">
            loading…
          </div>
        </Show>
      </div>
    </Show>
  );
}
