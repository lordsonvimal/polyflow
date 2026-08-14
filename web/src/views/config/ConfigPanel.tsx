import { Show, onMount } from "solid-js";
import { configStore } from "../../stores/config";
import FormMode from "./FormMode";
import YamlMode from "./YamlMode";

export default function ConfigPanel() {
  onMount(() => {
    configStore.load();
  });

  return (
    <div data-testid="config-panel" class="flex flex-col h-full min-h-0">
      <div class="px-3 py-2 border-b border-neutral-800 shrink-0 space-y-1">
        <div class="flex items-center gap-2">
          <span data-testid="config-path" class="text-xs text-neutral-400 truncate flex-1" title={configStore.path()}>
            {configStore.path() || "loading…"}
          </span>
          <div class="flex items-center border border-neutral-800 rounded overflow-hidden">
            <button
              data-testid="config-mode-form"
              class={`px-2 py-0.5 text-xs ${configStore.mode() === "form" ? "bg-neutral-700 text-white" : "text-neutral-500 hover:text-white"}`}
              onClick={() => configStore.setMode("form")}
            >
              Form
            </button>
            <button
              data-testid="config-mode-yaml"
              class={`px-2 py-0.5 text-xs ${configStore.mode() === "yaml" ? "bg-neutral-700 text-white" : "text-neutral-500 hover:text-white"}`}
              onClick={() => configStore.setMode("yaml")}
            >
              YAML
            </button>
          </div>
          <button
            data-testid="config-save"
            disabled={!configStore.dirty() || configStore.saving()}
            class="px-2 py-0.5 text-xs rounded bg-indigo-600 text-white disabled:opacity-40 disabled:cursor-not-allowed hover:bg-indigo-500"
            onClick={() => configStore.save()}
          >
            {configStore.saving() ? "Saving…" : "Save"}
          </button>
        </div>

        <Show when={configStore.parseError()}>
          <div data-testid="config-parse-error" class="text-[11px] text-red-400">
            Invalid YAML: {configStore.parseError()}
          </div>
        </Show>

        <Show when={configStore.diskChanged()}>
          <div data-testid="config-disk-changed-banner" class="flex items-center gap-2 text-[11px] bg-amber-950 text-amber-300 rounded px-2 py-1">
            <span class="flex-1">Config changed on disk.</span>
            <button data-testid="config-disk-reload" class="underline" onClick={() => configStore.reloadFromDiskBanner()}>
              reload
            </button>
            <button data-testid="config-disk-keep-editing" class="underline" onClick={() => configStore.dismissDiskChanged()}>
              keep editing
            </button>
          </div>
        </Show>

        <Show when={configStore.conflict()}>
          <div data-testid="config-conflict-banner" class="flex items-center gap-2 text-[11px] bg-amber-950 text-amber-300 rounded px-2 py-1">
            <span class="flex-1">Config changed on disk since you loaded it.</span>
            <button data-testid="config-conflict-keep-mine" class="underline" onClick={() => configStore.keepMine()}>
              keep mine
            </button>
            <button data-testid="config-conflict-take-disk" class="underline" onClick={() => configStore.takeDisk()}>
              take disk
            </button>
            <button data-testid="config-conflict-cancel" class="underline" onClick={() => configStore.cancelConflict()}>
              cancel
            </button>
          </div>
        </Show>

        <Show when={configStore.saveError()}>
          {(e) => (
            <div data-testid="config-save-error" class="text-[11px] bg-red-950 text-red-300 rounded px-2 py-1">
              <Show when={e().section}>
                <span class="font-semibold">[{e().section}] </span>
              </Show>
              {e().message}
            </div>
          )}
        </Show>

        <Show when={configStore.pendingWarning()}>
          {(w) => (
            <div data-testid="config-comment-warning" class="flex items-center gap-2 text-[11px] bg-amber-950 text-amber-300 rounded px-2 py-1">
              <span class="flex-1">
                Saving will remove comment(s) in: {w().sections.join(", ")} — continue?
              </span>
              <button data-testid="config-comment-warning-confirm" class="underline" onClick={() => configStore.confirmWarningAndSave()}>
                save anyway
              </button>
              <button data-testid="config-comment-warning-cancel" class="underline" onClick={() => configStore.cancelWarning()}>
                cancel
              </button>
            </div>
          )}
        </Show>
      </div>

      <div class="flex-1 min-h-0 flex flex-col overflow-hidden">
        <Show when={configStore.mode() === "form"} fallback={<YamlMode />}>
          <FormMode />
        </Show>
      </div>
    </div>
  );
}
