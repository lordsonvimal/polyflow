// CI.3: Dead-code activity — the UI counterpart of `polyflow deadcode` / the
// MCP `deadcode` tool. Lists function/method/component nodes with zero
// inbound invoking edges, variable/const nodes with zero inbound reads, and
// (DC.27) Rails ERB view files with zero inbound `renders` edges (GET
// /api/deadcode), scoped by service/file. Clicking a row selects the node so
// DetailHost/canvas show it, the same drill-in every other list view (Tree,
// search results) uses.
import { For, Show, onMount } from "solid-js";
import { deadcodeStore } from "../../stores/deadcode";
import { treeStore } from "../../stores/tree";
import { selectionStore } from "../../stores/selection";
import { displayLabel } from "../../lib/location";

export default function DeadcodePanel() {
  onMount(() => {
    void treeStore.loadServices();
    void deadcodeStore.load();
  });

  function applyFilters() {
    void deadcodeStore.load();
  }

  return (
    <div data-testid="deadcode-panel" class="p-3 overflow-y-auto h-full text-xs text-neutral-300 space-y-3">
      <div class="text-neutral-400">
        Function/method/component nodes with zero inbound <code class="text-neutral-500">calls</code>-family
        edges, variables with zero inbound <code class="text-neutral-500">reads</code>, and Rails ERB view
        files with zero inbound <code class="text-neutral-500">renders</code>, excluding recognized entry
        points (HTTP handlers, routes, workers, subscribers, gRPC/GraphQL handlers) and implicit Rails view
        resolution/dynamic render targets. A candidate list, not a certainty — dynamic dispatch, reflection,
        and exported public API can all show up here; verify before deleting.
      </div>

      <div class="flex items-center gap-2">
        <select
          data-testid="deadcode-service-filter"
          class="bg-neutral-800 rounded px-1.5 py-0.5 text-neutral-200"
          value={deadcodeStore.service()}
          onChange={(e) => {
            deadcodeStore.setService(e.currentTarget.value);
            applyFilters();
          }}
        >
          <option value="">All services</option>
          <For each={treeStore.services()}>{(s) => <option value={s.name}>{s.name}</option>}</For>
        </select>
        <input
          data-testid="deadcode-file-filter"
          class="flex-1 bg-neutral-800 rounded px-1.5 py-0.5 text-neutral-200"
          placeholder="filter by file path…"
          value={deadcodeStore.file()}
          onChange={(e) => {
            deadcodeStore.setFile(e.currentTarget.value);
            applyFilters();
          }}
        />
        <button
          data-testid="deadcode-refresh"
          class="px-2 py-0.5 rounded bg-neutral-700 hover:bg-neutral-600 text-neutral-200"
          onClick={applyFilters}
        >
          Refresh
        </button>
      </div>

      <Show when={deadcodeStore.loading() && !deadcodeStore.data()}>
        <div class="text-neutral-400">Scanning…</div>
      </Show>

      <Show when={deadcodeStore.error()}>
        <div data-testid="deadcode-error" class="text-red-400">
          {deadcodeStore.error()}
        </div>
      </Show>

      <Show when={deadcodeStore.data()}>
        {(d) => (
          <>
            <div data-testid="deadcode-total" class="text-neutral-400">
              {d().total} dead-code candidate{d().total === 1 ? "" : "s"}
            </div>
            <Show
              when={d().functions.length > 0}
              fallback={<div class="text-neutral-500">none found — nothing to clean up in this scope</div>}
            >
              <ul data-testid="deadcode-list" class="space-y-1">
                <For each={d().functions}>
                  {(f) => (
                    <li
                      data-testid="deadcode-row"
                      class="flex items-center gap-1.5 cursor-pointer hover:text-white"
                      onClick={() => selectionStore.setSelection({ kind: "node", id: f.id })}
                    >
                      <span class="px-1 rounded bg-neutral-800 text-neutral-400 text-[10px] font-semibold leading-none shrink-0">
                        {f.type}
                      </span>
                      <span class="text-neutral-200 truncate">{displayLabel(f.label)}</span>
                      <span class="text-neutral-500 truncate ml-auto shrink-0">
                        {f.service} · {f.file}:{f.line}
                      </span>
                    </li>
                  )}
                </For>
              </ul>
            </Show>
          </>
        )}
      </Show>
    </div>
  );
}
