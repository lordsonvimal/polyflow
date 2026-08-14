import { Show, Switch, Match } from "solid-js";
import { layoutPrefs } from "../stores/layoutPrefs";
import Resizer from "./Resizer";
import ExploreView from "../views/ExploreView";
import FlowsView from "../views/FlowsView";
import ImpactView from "../views/ImpactView";
import HealthView from "../views/HealthView";
import ConfigView from "../views/ConfigView";
import DocsView from "../views/DocsView";
import SettingsView from "../views/SettingsView";

export default function PanelHost() {
  const width = () => layoutPrefs.panelCollapsed() ? 0 : layoutPrefs.panelWidth();

  return (
    <div data-testid="panel-host" class="flex shrink-0" style={{ width: `${width()}px` }}>
      <Show when={!layoutPrefs.panelCollapsed()}>
        <div class="flex flex-col flex-1 min-h-0 border-r border-neutral-800 dark:border-neutral-700 bg-neutral-950">
          <div class="p-2 shrink-0">
            <button
              class="text-xs text-neutral-400 hover:text-white mb-2"
              onClick={() => layoutPrefs.setPanelCollapsed(true)}
            >
              ◀ collapse
            </button>
          </div>
          <div class="flex-1 min-h-0 overflow-hidden">
            <Switch>
              <Match when={layoutPrefs.activity() === "explore"}><ExploreView /></Match>
              <Match when={layoutPrefs.activity() === "flows"}><FlowsView /></Match>
              <Match when={layoutPrefs.activity() === "impact"}><ImpactView /></Match>
              <Match when={layoutPrefs.activity() === "health"}><HealthView /></Match>
              <Match when={layoutPrefs.activity() === "config"}><ConfigView /></Match>
              <Match when={layoutPrefs.activity() === "docs"}><DocsView /></Match>
              <Match when={layoutPrefs.activity() === "settings"}><SettingsView /></Match>
            </Switch>
          </div>
        </div>
        <Resizer />
      </Show>
      <Show when={layoutPrefs.panelCollapsed()}>
        <button
          class="w-6 flex items-center justify-center text-neutral-400 hover:text-white border-r border-neutral-800 dark:border-neutral-700 bg-neutral-950"
          onClick={() => layoutPrefs.setPanelCollapsed(false)}
        >
          ▶
        </button>
      </Show>
    </div>
  );
}
