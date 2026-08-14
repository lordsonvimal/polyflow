import { Show } from "solid-js";
import Catalog from "./flows/Catalog";
import WaypointBuilder from "./flows/WaypointBuilder";
import { waypointBuilderStore } from "../stores/waypointBuilder";

// UF.2: "Start flow here" seeds a waypoint session and switches this
// activity tab to it; the catalog is otherwise the tab's default content.
export default function FlowsView() {
  return (
    <Show when={waypointBuilderStore.isActive()} fallback={<Catalog />}>
      <WaypointBuilder />
    </Show>
  );
}
