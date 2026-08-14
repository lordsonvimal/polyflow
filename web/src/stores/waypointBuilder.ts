import { createSignal } from "solid-js";

export interface WaypointRef {
  id: string;
  label: string;
}

// UF.2: waypoint flow builder session state. "Start flow here" seeds the
// list (one chip); WaypointBuilder.tsx appends/removes chips and re-queries
// /api/flows/refine after every change. Kept as one flat store (rather than
// local component state) so the context-menu seed action and the FlowsView
// mount can share it without prop drilling, mirroring flowsThroughStore's
// bridge pattern.
const [waypoints, setWaypoints] = createSignal<WaypointRef[]>([]);
const [direction, setDirection] = createSignal<"forward" | "backward">("forward");
// One-shot: "Start flow here" seeds a fresh session and asks FlowsView to
// switch the active activity to "flows" so the builder is visible.
const [requestedSeed, setRequestedSeed] = createSignal<WaypointRef | null>(null);

export const waypointBuilderStore = {
  waypoints,
  direction,
  requestedSeed,

  requestStart: (ref: WaypointRef) => {
    setWaypoints([ref]);
    setDirection("forward");
    setRequestedSeed(ref);
  },
  consumeSeed: () => setRequestedSeed(null),

  append: (ref: WaypointRef) => {
    if (waypoints().some((w) => w.id === ref.id)) return;
    setWaypoints((ws) => [...ws, ref]);
  },
  prepend: (ref: WaypointRef) => {
    if (waypoints().some((w) => w.id === ref.id)) return;
    setWaypoints((ws) => [ref, ...ws]);
  },
  removeAt: (index: number) => setWaypoints((ws) => ws.filter((_, i) => i !== index)),
  setDirection: (d: "forward" | "backward") => setDirection(d),
  clear: () => setWaypoints([]),
  isActive: () => waypoints().length > 0,
};
