import { For, Show, onMount, onCleanup } from "solid-js";
import { fleetMembersStore } from "../../stores/fleetMembers";

// GR.6: fleet-member switcher — a dropdown next to FleetStatusPanel's
// per-member breakdown table that browses a DIFFERENT member's own graph
// (search/impact/context/trace all follow the active member), not just the
// cross-service edges into it that already show up regardless of which
// member is active. Renders nothing when this workspace isn't a registered
// Tier-GR fleet member (an empty services list), same "bonus, not a
// requirement" contract the backend endpoints follow.
export default function FleetSwitcher() {
  onMount(() => {
    fleetMembersStore.load();
    const unsubscribe = fleetMembersStore.subscribe();
    onCleanup(unsubscribe);
  });

  const activeService = () => fleetMembersStore.services().find((s) => s.active)?.service ?? "";

  return (
    <Show when={fleetMembersStore.services().length > 0}>
      <div data-testid="fleet-switcher" class="flex items-center gap-2 p-2 text-xs">
        <label for="fleet-switcher-select" class="text-neutral-400">
          Active member
        </label>
        <select
          id="fleet-switcher-select"
          data-testid="fleet-switcher-select"
          class="bg-neutral-800 border border-neutral-700 rounded px-2 py-1 text-neutral-200 disabled:opacity-50"
          value={activeService()}
          disabled={fleetMembersStore.switching()}
          onChange={(e) => fleetMembersStore.setActive(e.currentTarget.value)}
        >
          <For each={fleetMembersStore.services()}>{(s) => <option value={s.service}>{s.service}</option>}</For>
        </select>
        <Show when={fleetMembersStore.switching()}>
          <span data-testid="fleet-switcher-loading" class="text-neutral-500">
            switching…
          </span>
        </Show>
      </div>
    </Show>
  );
}
