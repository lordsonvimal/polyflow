// UO.6: which tab the Flows activity shows, and (for cross-links from the
// Health dashboard's coverage card) which session the Runtime tab should
// jump to when opened externally.
import { createSignal } from "solid-js";
import { layoutPrefs } from "./layoutPrefs";

export type FlowsTab = "catalog" | "runtime";

const [tab, setTab] = createSignal<FlowsTab>("catalog");
const [selectedSession, setSelectedSession] = createSignal<string | null>(null);

function openRuntime(session?: string): void {
  layoutPrefs.setActivity("flows");
  setTab("runtime");
  if (session) setSelectedSession(session);
}

export const runtimeViewStore = {
  tab,
  setTab,
  selectedSession,
  setSelectedSession,
  openRuntime,
};
