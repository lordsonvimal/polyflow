import { Component } from "solid-js";
import Graph from "./components/Graph";
import Toolbar from "./components/Toolbar";
import Search from "./components/Search";
import TracePanel from "./components/TracePanel";
import Detail from "./components/Detail";
import Filters from "./components/Filters";
import Legend from "./components/Legend";
import Notification from "./components/Notification";

const App: Component = () => {
  return (
    <div class="flex flex-col h-screen w-screen overflow-hidden bg-gray-950 text-gray-100">
      <Toolbar />

      <div class="flex flex-1 min-h-0">
        {/* Left sidebar */}
        <aside class="flex flex-col w-72 shrink-0 border-r border-gray-800 p-3 gap-4 overflow-y-auto">
          <Search />
          <TracePanel />
          <Filters />
          <Legend />
        </aside>

        {/* Main graph canvas */}
        <main class="flex-1 relative min-w-0">
          <Graph />
        </main>

        {/* Right detail panel */}
        <aside class="w-80 shrink-0 border-l border-gray-800 overflow-y-auto">
          <Detail />
        </aside>
      </div>

      {/* Toast notifications */}
      <Notification />
    </div>
  );
};

export default App;
