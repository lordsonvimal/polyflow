import { Component, Show, onMount, onCleanup } from "solid-js";
import ActivityBar from "./shell/ActivityBar";
import TopBar from "./shell/TopBar";
import PanelHost from "./shell/PanelHost";
import DetailHost from "./shell/DetailHost";
import BottomDrawer from "./shell/BottomDrawer";
import { scopeStore } from "./stores/scope";

const App: Component = () => {
  onMount(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") scopeStore.handleEsc();
    };
    window.addEventListener("keydown", handler);
    onCleanup(() => window.removeEventListener("keydown", handler));
  });

  return (
    <div class="flex flex-col h-screen w-screen overflow-hidden bg-neutral-950 text-neutral-100">
      <TopBar />
      <Show when={scopeStore.unknownVersionNotice()}>
        <div class="flex items-center gap-2 px-3 py-1 bg-amber-900/60 text-amber-200 text-xs shrink-0">
          <span>This link was created with a newer version of polyflow — view restored to default.</span>
          <button class="ml-auto hover:text-white" onClick={scopeStore.dismissVersionNotice}>×</button>
        </div>
      </Show>
      <Show when={scopeStore.staleIdNotice()}>
        <div class="flex items-center gap-2 px-3 py-1 bg-neutral-800 text-neutral-400 text-xs shrink-0">
          <span>A node from the saved view no longer exists after reindex — view reset to overview.</span>
          <button class="ml-auto hover:text-white" onClick={scopeStore.dismissStaleIdNotice}>×</button>
        </div>
      </Show>
      <div class="flex flex-1 min-h-0">
        <ActivityBar />
        <PanelHost />
        <div data-testid="canvas-host" class="flex-1 relative min-w-0 bg-neutral-900" />
        <DetailHost />
      </div>
      <BottomDrawer />
    </div>
  );
};

export default App;
