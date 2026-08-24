import { For, Show, createSignal } from "solid-js";
import PatternsPanel from "./patterns/PatternsPanel";
import AgentSetupPanel from "./setup/AgentSetupPanel";
import FleetStatusPanel from "./fleet/FleetStatusPanel";
import FleetSwitcher from "./fleet/FleetSwitcher";

type Section = "patterns" | "agents" | "fleet";

const SECTIONS: { id: Section; label: string }[] = [
  { id: "patterns", label: "Patterns" },
  { id: "agents", label: "Agents" },
  { id: "fleet", label: "Fleet" },
];

export default function SettingsView() {
  const [section, setSection] = createSignal<Section>("patterns");

  return (
    <div data-testid="settings-view" class="flex h-full min-h-0">
      <nav class="w-32 shrink-0 border-r border-neutral-800 p-2 space-y-0.5">
        <For each={SECTIONS}>
          {(s) => (
            <button
              data-testid={`settings-nav-${s.id}`}
              class={`w-full text-left px-2 py-1 rounded text-xs ${
                section() === s.id ? "bg-neutral-700 text-white" : "text-neutral-400 hover:text-white hover:bg-neutral-800"
              }`}
              onClick={() => setSection(s.id)}
            >
              {s.label}
            </button>
          )}
        </For>
      </nav>
      <div class="flex-1 min-w-0 min-h-0 overflow-y-auto p-2">
        <Show when={section() === "patterns"}>
          <PatternsPanel />
        </Show>
        <Show when={section() === "agents"}>
          <AgentSetupPanel />
        </Show>
        <Show when={section() === "fleet"}>
          <FleetSwitcher />
          <FleetStatusPanel />
        </Show>
      </div>
    </div>
  );
}
