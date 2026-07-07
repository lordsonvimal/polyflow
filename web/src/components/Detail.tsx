import { Component, Show, createSignal, createEffect } from "solid-js";
import { uiStore } from "../stores/ui";
import { GraphNode, GraphEdge } from "../stores/graph";

interface NodeDetail {
  node: GraphNode;
  edges_from: GraphEdge[];
  edges_to: GraphEdge[];
}

const Detail: Component = () => {
  const [detail, setDetail] = createSignal<NodeDetail | null>(null);
  const [source, setSource] = createSignal<string | null>(null);
  const [loadingSource, setLoadingSource] = createSignal(false);

  createEffect(async () => {
    const id = uiStore.selectedNodeId();
    setSource(null);
    if (!id) {
      setDetail(null);
      return;
    }
    const res = await fetch(`/api/node/${encodeURIComponent(id)}`);
    if (!res.ok) {
      setDetail(null);
      return;
    }
    setDetail(await res.json());
  });

  async function loadSource() {
    const id = uiStore.selectedNodeId();
    if (!id) return;
    setLoadingSource(true);
    try {
      const res = await fetch(`/api/node/${encodeURIComponent(id)}/source`);
      if (!res.ok) return;
      const data = await res.json();
      setSource(data.source ?? null);
    } finally {
      setLoadingSource(false);
    }
  }

  return (
    <div class="p-4">
      <Show when={detail()} fallback={<p class="text-sm text-gray-500">Select a node to see details.</p>}>
        {(d) => (
          <div class="flex flex-col gap-3">
            <h2 class="text-sm font-semibold text-indigo-300 truncate">{d().node.label}</h2>
            <dl class="text-xs text-gray-300 space-y-1">
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Type</dt><dd>{d().node.type}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Service</dt><dd>{d().node.service}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">File</dt><dd class="truncate">{d().node.file}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Line</dt><dd>{d().node.line}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Lang</dt><dd>{(d().node as any).language ?? ""}</dd></div>
            </dl>

            <div class="text-xs text-gray-500">
              <span class="text-gray-400">{d().edges_from?.length ?? 0}</span> outgoing &nbsp;
              <span class="text-gray-400">{d().edges_to?.length ?? 0}</span> incoming
            </div>

            <Show when={!source()}>
              <button
                class="text-xs text-indigo-400 hover:text-indigo-300 self-start"
                onClick={loadSource}
                disabled={loadingSource()}
              >
                {loadingSource() ? "Loading…" : "Show source"}
              </button>
            </Show>

            <Show when={source()}>
              <pre class="text-xs text-gray-300 bg-gray-900 rounded p-2 overflow-x-auto max-h-96 whitespace-pre-wrap break-all">
                {source()}
              </pre>
            </Show>
          </div>
        )}
      </Show>
    </div>
  );
};

export default Detail;
