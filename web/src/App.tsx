import { Component, ErrorBoundary, Show, onMount, onCleanup } from "solid-js";
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
import { jobsStore } from "./stores/jobs";
import { setupStore } from "./stores/setup";
import { fleetMembersStore } from "./stores/fleetMembers";
import SetupView from "./views/SetupView";

const App: Component = () => {
  onMount(() => {
    const cleanup = registerKeys(window);
    connectionStore.start();
    setupStore.checkStatus();
    // Drives the "Syncing fleet data…" banner below — must be app-wide, not
    // tied to FleetSwitcher's own mount (Settings > Fleet), since the banner
    // needs to show on any view, including the overview graph most users
    // land on.
    const unsubscribeFleet = fleetMembersStore.subscribe();
    onCleanup(() => {
      cleanup();
      connectionStore.stop();
      unsubscribeFleet();
    });
  });

  const needsSetup = () => {
    const s = setupStore.status();
    return s != null && (s.needs_config || s.needs_index);
  };

  return (
    <Show when={needsSetup()} fallback={<AppShell />}>
      <SetupView />
    </Show>
  );
};

const AppShell: Component = () => {
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
      <Show when={fleetMembersStore.syncing()}>
        <div data-testid="fleet-syncing-banner" class="flex items-center gap-2 px-3 py-1 bg-neutral-800 text-neutral-300 text-xs shrink-0">
          <span class="animate-pulse">Syncing fleet data…</span>
        </div>
      </Show>
      <Show when={jobsStore.reloadBanner()}>
        <div data-testid="reload-banner" class="flex items-center gap-2 px-3 py-1 bg-indigo-900/60 text-indigo-200 text-xs shrink-0">
          <span>Graph updated — Reload view</span>
          <button data-testid="reload-banner-action" class="hover:text-white underline" onClick={jobsStore.reloadView}>
            Reload view
          </button>
          <button class="ml-auto hover:text-white" onClick={jobsStore.dismissReloadBanner}>×</button>
        </div>
      </Show>
      <div class="flex flex-1 min-h-0">
        <ActivityBar />
        <PanelHost />
        {/* An uncaught throw from any of CanvasHost's effects/memos (a resource
            reading its own error-state accessor unsafely, a Cytoscape call
            against stale data, etc.) otherwise wedges the canvas permanently:
            Solid re-throws computation errors when no ErrorBoundary is
            present, which can leave the reactive graph unable to schedule
            further updates for this subtree — the resource's own loading
            state gets stuck true forever and nothing (not even navigating to
            an unrelated, valid scope) recovers it. This boundary turns that
            into a recoverable state instead of a dead canvas: reset() clears
            the boundary and remounts CanvasHost fresh, and scopeStore.reset()
            ensures it remounts onto a known-good scope rather than
            re-triggering the same broken one. */}
        <ErrorBoundary
          fallback={(err, reset) => (
            <div class="flex-1 min-w-0 flex flex-col items-center justify-center gap-3 bg-neutral-950 text-neutral-400 p-6">
              <span class="text-sm text-center max-w-md">
                Canvas hit an unexpected error and can't continue: {String((err as Error)?.message ?? err)}
              </span>
              <button
                class="px-3 py-1 rounded bg-neutral-700 hover:bg-neutral-600 text-white text-xs"
                onClick={() => {
                  scopeStore.reset();
                  reset();
                }}
              >
                Reset canvas
              </button>
            </div>
          )}
        >
          <CanvasHost />
        </ErrorBoundary>
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
