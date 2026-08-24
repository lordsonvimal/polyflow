import { For, Show, onMount } from "solid-js";
import { setupStore } from "../../stores/setup";

// UO.8: the same registry-backed "known workspaces" list SetupView shows
// when a fresh `polyflow serve` starts outside any configured repo, but
// reachable at any time from Settings — otherwise there'd be no way back to
// it once a workspace has been opened once (SetupView only renders while
// needs_config/needs_index is true). Selecting an entry restarts the server
// process the same way (POST /api/setup/select) and reloads this page once
// it comes back up.
export default function WorkspaceSwitcher() {
  onMount(() => {
    setupStore.loadRegistry();
  });

  const currentPath = () => setupStore.status()?.config_path;

  return (
    <Show when={setupStore.registryEntries().length > 0} fallback={<EmptyState />}>
      <div data-testid="workspace-switcher" class="p-2 text-xs">
        <div class="text-neutral-400 mb-1">Known workspaces on this machine</div>
        <Show when={setupStore.selectError()}>
          <div data-testid="workspace-switcher-error" class="text-red-400 mb-1">
            {setupStore.selectError()}
          </div>
        </Show>
        <ul class="flex flex-col gap-1">
          <For each={setupStore.registryEntries()}>
            {(entry) => {
              const isCurrent = () => currentPath()?.startsWith(entry.local_path);
              return (
                <li data-testid="workspace-switcher-row" class="flex items-center justify-between gap-2 py-0.5">
                  <span class="truncate">
                    <span class="text-neutral-200 font-mono">{entry.service}</span> — {entry.local_path}
                  </span>
                  <Show
                    when={!isCurrent()}
                    fallback={
                      <span class="shrink-0 text-neutral-500" data-testid="workspace-switcher-current">
                        current
                      </span>
                    }
                  >
                    <button
                      data-testid="workspace-switcher-open"
                      class="shrink-0 px-2 py-0.5 rounded bg-neutral-700 hover:bg-neutral-600 text-neutral-200 disabled:opacity-50"
                      disabled={setupStore.selecting() !== null}
                      onClick={() => setupStore.selectWorkspace(entry.local_path)}
                    >
                      {setupStore.selecting() === entry.local_path ? "Opening…" : "Open"}
                    </button>
                  </Show>
                </li>
              );
            }}
          </For>
        </ul>
      </div>
    </Show>
  );
}

function EmptyState() {
  return (
    <div data-testid="workspace-switcher-empty" class="p-2 text-xs text-neutral-500">
      No other known workspaces on this machine yet — run `polyflow index` in a repo to register it.
    </div>
  );
}
