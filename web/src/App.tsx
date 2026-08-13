import { Component, Show, onMount, onCleanup } from "solid-js";
import ActivityBar from "./shell/ActivityBar";
import TopBar from "./shell/TopBar";
import PanelHost from "./shell/PanelHost";
import DetailHost from "./shell/DetailHost";
import BottomDrawer from "./shell/BottomDrawer";
import ContextMenu from "./interaction/ContextMenu";
import HoverTooltip from "./shell/HoverTooltip";
import ConnectionBanner from "./shell/ConnectionBanner";
import Toasts from "./shell/Toasts";
import CanvasHost from "./views/canvas/CanvasHost";
import Palette from "./views/palette/Palette";
import { scopeStore } from "./stores/scope";
import { registerKeys } from "./interaction/keys";
import { connectionStore } from "./stores/connection";

const App: Component = () => {
  onMount(() => {
    const cleanup = registerKeys(window);
    connectionStore.start();
    onCleanup(() => {
      cleanup();
      connectionStore.stop();
    });
  });

  return (
    <div class="flex flex-col h-screen w-screen overflow-hidden bg-neutral-950 text-neutral-100">
      <TopBar />
      <ConnectionBanner />
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
        <CanvasHost />
        <DetailHost />
      </div>
      <BottomDrawer />
      <ContextMenu />
      <HoverTooltip />
      <Palette />
      <Toasts />
    </div>
  );
};

export default App;
