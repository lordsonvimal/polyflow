import { Show } from "solid-js";
import Catalog from "./flows/Catalog";
import WaypointBuilder from "./flows/WaypointBuilder";
import RuntimeTab from "./flows/RuntimeTab";
import { waypointBuilderStore } from "../stores/waypointBuilder";
import { runtimeViewStore } from "../stores/runtimeView";

// UF.2: "Start flow here" seeds a waypoint session and switches this
// activity tab to it, short-circuiting the Catalog/Runtime tab bar below.
// UO.6: Catalog/Runtime are otherwise selectable via a small tab bar.
export default function FlowsView() {
  return (
    <Show when={waypointBuilderStore.isActive()} fallback={<FlowsTabs />}>
      <WaypointBuilder />
    </Show>
  );
}

function FlowsTabs() {
  return (
    <div class="flex flex-col h-full">
      <div data-testid="flows-tab-bar" class="flex items-center gap-1 px-2 py-1 border-b border-neutral-800 text-xs shrink-0">
        <button
          data-testid="flows-tab-catalog"
          class={`px-2 py-1 rounded ${
            runtimeViewStore.tab() === "catalog" ? "bg-neutral-800 text-white" : "text-neutral-400 hover:text-white"
          }`}
          onClick={() => runtimeViewStore.setTab("catalog")}
        >
          Catalog
        </button>
        <button
          data-testid="flows-tab-runtime"
          class={`px-2 py-1 rounded ${
            runtimeViewStore.tab() === "runtime" ? "bg-neutral-800 text-white" : "text-neutral-400 hover:text-white"
          }`}
          onClick={() => runtimeViewStore.setTab("runtime")}
        >
          Runtime
        </button>
      </div>
      <div class="flex-1 min-h-0">
        <Show when={runtimeViewStore.tab() === "runtime"} fallback={<Catalog />}>
          <RuntimeTab />
        </Show>
      </div>
    </div>
  );
}
