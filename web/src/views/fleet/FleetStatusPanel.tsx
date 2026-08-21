import { For, Show, onMount } from "solid-js";
import { fleetStatusStore } from "../../stores/fleetStatus";

// FR.7: Fleet status panel, surfaced from Settings — per-service staleness
// now that each service has its own independently-timestamped graph.db
// (FR.2/FR.3), where before this plan the whole fleet DB shared one
// last_indexed value.
function formatIndexedAt(iso?: string): string {
  if (!iso) return "never indexed on its own";
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return iso;
  return t.toLocaleString();
}

export default function FleetStatusPanel() {
  onMount(() => {
    fleetStatusStore.load();
  });

  return (
    <div data-testid="fleet-status-panel" class="text-xs">
      <Show when={fleetStatusStore.loading()}>
        <div class="p-3 text-neutral-400">Loading…</div>
      </Show>
      <Show when={!fleetStatusStore.loading() && fleetStatusStore.services().length === 0}>
        <div class="p-3 text-neutral-500">No services configured.</div>
      </Show>
      <Show when={!fleetStatusStore.loading() && fleetStatusStore.services().length > 0}>
        <table class="w-full border-collapse">
          <thead>
            <tr class="text-left text-neutral-500 border-b border-neutral-800">
              <th class="p-2 font-normal">Service</th>
              <th class="p-2 font-normal">Indexed</th>
              <th class="p-2 font-normal">Nodes</th>
              <th class="p-2 font-normal">Edges</th>
            </tr>
          </thead>
          <tbody>
            <For each={fleetStatusStore.services()}>
              {(s) => (
                <tr data-testid="fleet-status-row" class="border-b border-neutral-900">
                  <td class="p-2 text-neutral-200 font-mono">{s.service}</td>
                  <td data-testid="fleet-status-indexed-at" class={`p-2 ${s.indexed ? "text-neutral-300" : "text-neutral-600"}`}>
                    {formatIndexedAt(s.indexed_at)}
                  </td>
                  <td class="p-2 text-neutral-400">{s.indexed ? s.node_count : "—"}</td>
                  <td class="p-2 text-neutral-400">{s.indexed ? s.edge_count : "—"}</td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </Show>
    </div>
  );
}
