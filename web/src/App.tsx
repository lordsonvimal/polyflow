import { Component } from "solid-js";
import ActivityBar from "./shell/ActivityBar";
import TopBar from "./shell/TopBar";
import PanelHost from "./shell/PanelHost";
import DetailHost from "./shell/DetailHost";
import BottomDrawer from "./shell/BottomDrawer";

const App: Component = () => {
  return (
    <div class="flex flex-col h-screen w-screen overflow-hidden bg-neutral-950 text-neutral-100">
      <TopBar />
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
