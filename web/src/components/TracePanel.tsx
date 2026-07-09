import { Component, Show, createMemo } from "solid-js";
import { graphStore, TraceDirection } from "../stores/graph";

// TracePanel appears while a trace is active: it names the root, lets the
// user change direction/depth in place, and offers the way back to the full
// graph. This completes the search → root-select → isolated-subgraph flow.
const TracePanel: Component = () => {
  const rootNode = createMemo(() => {
    const id = graphStore.traceRoot();
    if (!id) return null;
    return graphStore.nodes().find((n) => n.id === id) ?? { id, label: id, service: "" };
  });

  return (
    <Show when={graphStore.traceRoot()}>
      <div class="flex flex-col gap-2 rounded border border-pink-900/60 bg-pink-950/20 p-2">
        <div class="flex items-center justify-between">
          <span class="text-xs font-semibold text-pink-300 uppercase tracking-wide">Trace</span>
          <button
            class="text-xs text-gray-400 hover:text-white cursor-pointer"
            onClick={() => graphStore.clearTrace()}
            title="Back to full graph"
          >
            ✕ clear
          </button>
        </div>
        <div class="text-xs text-gray-300 truncate" title={rootNode()?.id}>
          root: <span class="text-pink-200 font-medium">{rootNode()?.label}</span>
          <Show when={rootNode()?.service}>
            <span class="text-gray-500 ml-1">({rootNode()?.service})</span>
          </Show>
        </div>
        <div class="flex items-center gap-2">
          <select
            class="bg-gray-800 border border-gray-700 rounded px-1 py-0.5 text-xs text-gray-200"
            value={graphStore.traceDirection()}
            onChange={(e) => graphStore.retrace(e.currentTarget.value as TraceDirection)}
          >
            <option value="both">both</option>
            <option value="forward">forward</option>
            <option value="backward">backward</option>
          </select>
          <label class="text-xs text-gray-500">depth</label>
          <input
            type="number"
            min="1"
            max="50"
            class="w-14 bg-gray-800 border border-gray-700 rounded px-1 py-0.5 text-xs text-gray-200"
            value={graphStore.traceDepth()}
            onChange={(e) => {
              const d = parseInt(e.currentTarget.value, 10);
              if (d > 0) graphStore.retrace(undefined, d);
            }}
          />
        </div>
      </div>
    </Show>
  );
};

export default TracePanel;
