import { Component } from "solid-js";
import Graph from "./components/Graph";
import Search from "./components/Search";
import Detail from "./components/Detail";
import Filters from "./components/Filters";
import LayoutToggle from "./components/LayoutToggle";
import Notification from "./components/Notification";

const App: Component = () => {
  return (
    <div class="flex h-screen w-screen overflow-hidden bg-gray-950 text-gray-100">
      {/* Left sidebar */}
      <aside class="flex flex-col w-72 shrink-0 border-r border-gray-800 p-3 gap-3">
        <h1 class="text-lg font-bold tracking-tight text-indigo-400">polyflow</h1>
        <Search />
        <Filters />
        <LayoutToggle />
      </aside>

      {/* Main graph canvas */}
      <main class="flex-1 relative">
        <Graph />
      </main>

      {/* Right detail panel */}
      <aside class="w-80 shrink-0 border-l border-gray-800 overflow-y-auto">
        <Detail />
      </aside>

      {/* Toast notifications */}
      <Notification />
    </div>
  );
};

export default App;
