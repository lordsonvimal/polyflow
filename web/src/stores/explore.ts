import { createSignal } from "solid-js";

// Explore panel's own sub-tab (Tree vs Stack), lifted out of ExploreView so
// other views (DetailHost's service-node "view in tech stack" link, UN.4)
// can switch tabs without owning the panel.
export type ExploreTab = "tree" | "stack" | "views";

const [tab, setTab] = createSignal<ExploreTab>("tree");
// Set alongside the "stack" tab switch so StackPanel can scroll/highlight
// the service card that triggered the navigation; cleared once read.
const [focusService, setFocusService] = createSignal<string | undefined>(undefined);

function openStackFor(service: string): void {
  setFocusService(service);
  setTab("stack");
}

function clearFocusService(): void {
  setFocusService(undefined);
}

// Test-only: clears module-singleton state between test cases.
function reset(): void {
  setTab("tree");
  setFocusService(undefined);
}

export const exploreStore = {
  tab,
  setTab,
  focusService,
  openStackFor,
  clearFocusService,
  reset,
};
