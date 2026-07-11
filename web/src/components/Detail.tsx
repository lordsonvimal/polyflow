import { Component, Show, For, createSignal, createEffect, createMemo } from "solid-js";
import { uiStore } from "../stores/ui";
import { graphStore } from "../stores/graph";
import { boundaryGroups, fileGroups } from "../stores/derived";
import { GraphNode, GraphEdge } from "../lib/types";
import { isBoundaryGroupId } from "../lib/boundary";
import { isFileGroupId, fileGroupId } from "../lib/filegroup";
import { isServiceNodeId, serviceFromNodeId } from "../lib/aggregate";
import { edgeConfidence } from "../lib/confidence";

interface NodeDetail {
  node: GraphNode;
  edges_from: { id: string; from: string; to: string; type: string; confidence?: string; meta?: Record<string, string> }[];
  edges_to: { id: string; from: string; to: string; type: string; confidence?: string; meta?: Record<string, string> }[];
}

// versionChip renders `package@resolved_version` for framework-boundary and
// cloud-SDK nodes — the "this S3 upload uses SDK v1" affordance.
const VersionChip: Component<{ meta?: Record<string, string> }> = (props) => (
  <Show when={props.meta?.package}>
    <span class="inline-block rounded bg-indigo-950 border border-indigo-700 text-indigo-300 text-[10px] px-1.5 py-0.5 font-mono">
      {props.meta!.package}
      {props.meta?.resolved_version ? `@${props.meta.resolved_version}` : ""}
    </span>
  </Show>
);

// RootKindBadge surfaces the Phase 0.4 root classification for functions with
// no in-service caller: entrypoint (run by the runtime), callback (referenced /
// framework-invoked), or unreachable (dead-code candidate). Called functions
// carry no root_kind, so the badge is absent for them.
const ROOT_KIND_STYLES: Record<string, { cls: string; title: string }> = {
  entrypoint: {
    cls: "bg-emerald-950 border-emerald-700 text-emerald-300",
    title: "Entrypoint — invoked by the runtime (main/init/handler)",
  },
  callback: {
    cls: "bg-amber-950 border-amber-700 text-amber-300",
    title: "Callback — referenced or satisfies an interface invoked by a framework",
  },
  unreachable: {
    cls: "bg-red-950 border-red-700 text-red-300",
    title: "Unreachable — nothing references it (dead-code candidate)",
  },
};
const RootKindBadge: Component<{ meta?: Record<string, string> }> = (props) => {
  const rk = () => props.meta?.root_kind;
  return (
    <Show when={rk() && ROOT_KIND_STYLES[rk()!]}>
      <span
        class={`inline-block rounded border text-[10px] px-1.5 py-0.5 font-mono ${ROOT_KIND_STYLES[rk()!].cls}`}
        title={ROOT_KIND_STYLES[rk()!].title}
      >
        {rk()}
      </span>
    </Show>
  );
};

const ConfidenceBadge: Component<{ edge: { confidence?: string } }> = (props) => {
  const c = () => edgeConfidence(props.edge);
  const cls = () =>
    c() === "static"
      ? "text-emerald-400"
      : c() === "inferred"
        ? "text-sky-400"
        : "text-amber-400";
  return <span class={`text-[10px] ${cls()}`}>{c()}</span>;
};

interface FlowRef {
  id: string;
  label: string;
  file: string;
  line: number;
  meta?: Record<string, string>;
}

interface VariableFlow {
  readers: FlowRef[] | null;
  writers: FlowRef[] | null;
  captured_by: FlowRef[] | null;
  flows_to: FlowRef[] | null;
}

interface FileImpactEntry {
  file: string;
  service: string;
  nodes: number;
  min_depth: number;
  edge_types: string[];
}

// CopyPath writes a file path to the clipboard with brief inline feedback.
const CopyPath: Component<{ path: string }> = (props) => {
  const [copied, setCopied] = createSignal(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(props.path);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      uiStore.setNotification("Copy failed — clipboard unavailable");
    }
  }
  return (
    <button
      class="rounded bg-gray-800 hover:bg-gray-700 text-gray-300 text-xs px-2 py-1 cursor-pointer"
      title={props.path}
      onClick={copy}
    >
      {copied() ? "Copied ✓" : "Copy path"}
    </button>
  );
};

const Detail: Component = () => {
  const [detail, setDetail] = createSignal<NodeDetail | null>(null);
  const [source, setSource] = createSignal<string | null>(null);
  const [loadingSource, setLoadingSource] = createSignal(false);
  const [fileImpact, setFileImpact] = createSignal<{ direction: string; entries: FileImpactEntry[] } | null>(null);
  const [loadingImpact, setLoadingImpact] = createSignal(false);
  const [varFlow, setVarFlow] = createSignal<VariableFlow | null>(null);

  const selectedId = () => uiStore.selectedNodeId();

  const selectedGroup = createMemo(() => {
    const id = selectedId();
    if (!id || !isBoundaryGroupId(id)) return null;
    return boundaryGroups().find((g) => g.id === id) ?? null;
  });

  const selectedFile = createMemo(() => {
    const id = selectedId();
    if (!id || !isFileGroupId(id)) return null;
    return fileGroups().find((g) => g.id === id) ?? null;
  });

  async function loadFileImpact(direction: "forward" | "backward" | "both") {
    const g = selectedFile();
    if (!g) return;
    setLoadingImpact(true);
    try {
      const sp = new URLSearchParams({ path: g.file, service: g.service, direction });
      const res = await fetch(`/api/file/impact?${sp.toString()}`);
      if (!res.ok) {
        uiStore.setNotification("File impact query failed");
        return;
      }
      const data = await res.json();
      setFileImpact({ direction, entries: data.impacted ?? [] });
    } finally {
      setLoadingImpact(false);
    }
  }

  const selectedService = createMemo(() => {
    const id = selectedId();
    if (!id || !isServiceNodeId(id)) return null;
    const svc = serviceFromNodeId(id);
    const members = graphStore.nodes().filter((n) => n.service === svc);
    const byType = new Map<string, number>();
    for (const n of members) byType.set(n.type, (byType.get(n.type) ?? 0) + 1);
    return { service: svc, total: members.length, byType: [...byType.entries()].sort() };
  });

  createEffect(async () => {
    const id = selectedId();
    setSource(null);
    setDetail(null);
    setFileImpact(null);
    setVarFlow(null);
    if (!id || isBoundaryGroupId(id) || isFileGroupId(id) || isServiceNodeId(id)) return;
    const res = await fetch(`/api/node/${encodeURIComponent(id)}`);
    if (!res.ok) return;
    const d: NodeDetail = await res.json();
    setDetail(d);
    if (d.node.type === "variable") {
      const fres = await fetch(`/api/variable/${encodeURIComponent(id)}/flow`);
      if (fres.ok) setVarFlow(await fres.json());
    }
  });

  async function loadSource() {
    const id = selectedId();
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

  const otherEnd = (e: { from: string; to: string }, own: string) =>
    e.from === own ? e.to : e.from;

  const nodeLabel = (id: string) => graphStore.nodes().find((n) => n.id === id)?.label ?? id;

  const edgeRow = (e: { id: string; from: string; to: string; type: string; confidence?: string }, own: string, outgoing: boolean) => (
    <li class="flex items-center gap-1.5 text-xs">
      <span class="text-gray-600">{outgoing ? "→" : "←"}</span>
      <span class="text-gray-500 shrink-0">[{e.type}]</span>
      <button
        class="text-gray-300 hover:text-indigo-300 truncate cursor-pointer text-left"
        title={otherEnd(e, own)}
        onClick={() => uiStore.setSelectedNodeId(otherEnd(e, own))}
      >
        {nodeLabel(otherEnd(e, own))}
      </button>
      <ConfidenceBadge edge={e} />
    </li>
  );

  return (
    <div class="p-4 overflow-y-auto h-full">
      {/* Boundary group panel */}
      <Show when={selectedGroup()}>
        {(g) => (
          <div class="flex flex-col gap-3">
            <h2 class="text-sm font-semibold text-indigo-300">Framework boundary</h2>
            <VersionChip meta={{ package: g().package, resolved_version: g().version }} />
            <dl class="text-xs text-gray-300 space-y-1">
              <div class="flex gap-2"><dt class="text-gray-500 w-20 shrink-0">Service</dt><dd>{g().service}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-20 shrink-0">Call sites</dt><dd>{g().members.length}</dd></div>
            </dl>
            <button
              class="self-start rounded bg-indigo-700 hover:bg-indigo-600 text-white text-xs px-2 py-1 cursor-pointer"
              onClick={() => uiStore.toggleBoundary(g().id)}
            >
              {uiStore.expandedBoundaries().includes(g().id) ? "Collapse group" : "Expand group"}
            </button>
            <ul class="flex flex-col gap-1 text-xs text-gray-400">
              <For each={g().members}>
                {(m) => (
                  <li class="truncate" title={`${m.file}:${m.line}`}>
                    {m.label} <span class="text-gray-600">{m.file}:{m.line}</span>
                  </li>
                )}
              </For>
            </ul>
          </div>
        )}
      </Show>

      {/* File group panel */}
      <Show when={selectedFile()}>
        {(g) => (
          <div class="flex flex-col gap-3">
            <h2 class="text-sm font-semibold text-indigo-300 break-all">{g().file}</h2>
            <dl class="text-xs text-gray-300 space-y-1">
              <div class="flex gap-2"><dt class="text-gray-500 w-20 shrink-0">Service</dt><dd>{g().service}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-20 shrink-0">Nodes</dt><dd>{g().members.length}</dd></div>
            </dl>
            <div class="flex gap-1 flex-wrap">
              <CopyPath path={g().file} />
              <button
                class="rounded bg-gray-800 hover:bg-gray-700 text-gray-300 text-xs px-2 py-1 cursor-pointer"
                onClick={() => loadFileImpact("forward")}
                disabled={loadingImpact()}
              >
                ↓ downstream
              </button>
              <button
                class="rounded bg-gray-800 hover:bg-gray-700 text-gray-300 text-xs px-2 py-1 cursor-pointer"
                onClick={() => loadFileImpact("backward")}
                disabled={loadingImpact()}
              >
                ↑ upstream
              </button>
              <button
                class="rounded bg-gray-800 hover:bg-gray-700 text-gray-300 text-xs px-2 py-1 cursor-pointer"
                onClick={() => uiStore.toggleFileCollapse(g().id)}
              >
                {uiStore.collapsedFiles().includes(g().id) ? "Expand file" : "Collapse file"}
              </button>
            </div>

            <Show when={loadingImpact()}>
              <span class="text-[10px] text-gray-500">computing impact…</span>
            </Show>
            <Show when={fileImpact()}>
              {(imp) => (
                <div>
                  <h3 class="text-[10px] uppercase tracking-wide text-gray-500 mb-1">
                    {imp().direction === "forward" ? "Downstream" : imp().direction === "backward" ? "Upstream" : "Impacted"} files ({imp().entries.length})
                  </h3>
                  <Show when={imp().entries.length === 0}>
                    <p class="text-xs text-gray-500">No impacted files within depth.</p>
                  </Show>
                  <ul class="flex flex-col gap-1 text-xs">
                    <For each={imp().entries}>
                      {(e) => (
                        <li class="flex items-center gap-1.5">
                          <span class="text-gray-600 shrink-0">d{e.min_depth}</span>
                          <button
                            class="text-gray-300 hover:text-indigo-300 truncate cursor-pointer text-left"
                            title={`${e.file} — ${e.nodes} nodes via ${e.edge_types.join(", ")}`}
                            onClick={() => uiStore.setSelectedNodeId(fileGroupId(e.service, e.file))}
                          >
                            {e.file}
                          </button>
                          <span class="text-gray-600 shrink-0">{e.nodes}</span>
                        </li>
                      )}
                    </For>
                  </ul>
                </div>
              )}
            </Show>

            <ul class="flex flex-col gap-1 text-xs text-gray-400">
              <For each={g().members}>
                {(m) => (
                  <li class="truncate" title={`${m.file}:${m.line}`}>
                    <button
                      class="hover:text-indigo-300 cursor-pointer"
                      onClick={() => uiStore.setSelectedNodeId(m.id)}
                    >
                      {m.label}
                    </button>
                    <span class="text-gray-600 ml-1">:{m.line} · {m.type}</span>
                  </li>
                )}
              </For>
            </ul>
          </div>
        )}
      </Show>

      {/* High-level service panel */}
      <Show when={selectedService()}>
        {(s) => (
          <div class="flex flex-col gap-3">
            <h2 class="text-sm font-semibold text-indigo-300">{s().service}</h2>
            <p class="text-xs text-gray-400">{s().total} nodes</p>
            <dl class="text-xs text-gray-300 space-y-1">
              <For each={s().byType}>
                {([type, count]) => (
                  <div class="flex gap-2">
                    <dt class="text-gray-500 flex-1 truncate">{type}</dt>
                    <dd>{count}</dd>
                  </div>
                )}
              </For>
            </dl>
            <p class="text-[10px] text-gray-600">Switch to the in-depth view to inspect individual nodes.</p>
          </div>
        )}
      </Show>

      {/* Regular node panel */}
      <Show
        when={detail()}
        fallback={
          <Show when={!selectedGroup() && !selectedFile() && !selectedService()}>
            <p class="text-sm text-gray-500">Select a node to see details.</p>
          </Show>
        }
      >
        {(d) => (
          <div class="flex flex-col gap-3">
            <h2 class="text-sm font-semibold text-indigo-300 break-all">{d().node.label}</h2>
            <div class="flex gap-1 flex-wrap">
              <VersionChip meta={d().node.meta} />
              <RootKindBadge meta={d().node.meta} />
            </div>
            <dl class="text-xs text-gray-300 space-y-1">
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Type</dt><dd>{d().node.type}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Service</dt><dd>{d().node.service}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">File</dt><dd class="truncate" title={d().node.file}>{d().node.file}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Line</dt><dd>{d().node.line}</dd></div>
              <Show when={d().node.language}>
                <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Lang</dt><dd>{d().node.language}</dd></div>
              </Show>
            </dl>

            {/* Trace from here */}
            <div class="flex gap-1 flex-wrap">
              <CopyPath path={d().node.file} />
              <button
                class="rounded bg-pink-800 hover:bg-pink-700 text-white text-xs px-2 py-1 cursor-pointer"
                onClick={() => graphStore.fetchTrace(d().node.id, "both", graphStore.traceDepth())}
              >
                Trace
              </button>
              <button
                class="rounded bg-gray-800 hover:bg-gray-700 text-gray-300 text-xs px-2 py-1 cursor-pointer"
                onClick={() => graphStore.fetchTrace(d().node.id, "forward", graphStore.traceDepth())}
              >
                ↓ downstream
              </button>
              <button
                class="rounded bg-gray-800 hover:bg-gray-700 text-gray-300 text-xs px-2 py-1 cursor-pointer"
                onClick={() => graphStore.fetchTrace(d().node.id, "backward", graphStore.traceDepth())}
              >
                ↑ upstream
              </button>
            </div>

            {/* Metadata */}
            <Show when={d().node.meta && Object.keys(d().node.meta!).length > 0}>
              <details class="text-xs">
                <summary class="text-gray-400 cursor-pointer select-none">Metadata</summary>
                <dl class="mt-1 space-y-0.5">
                  <For each={Object.entries(d().node.meta!).sort()}>
                    {([k, v]) => (
                      <div class="flex gap-2">
                        <dt class="text-gray-500 w-28 shrink-0 truncate" title={k}>{k}</dt>
                        <dd class="text-gray-300 break-all">{v}</dd>
                      </div>
                    )}
                  </For>
                </dl>
              </details>
            </Show>

            {/* Variable flow summary */}
            <Show when={varFlow()}>
              {(vf) => {
                const section = (title: string, refs: FlowRef[] | null, metaKey?: string) => (
                  <Show when={(refs?.length ?? 0) > 0}>
                    <div>
                      <h3 class="text-[10px] uppercase tracking-wide text-gray-500 mb-1">
                        {title} ({refs!.length})
                      </h3>
                      <ul class="flex flex-col gap-0.5">
                        <For each={refs!}>
                          {(r) => (
                            <li class="flex items-center gap-1.5 text-xs">
                              <button
                                class="text-gray-300 hover:text-indigo-300 truncate cursor-pointer text-left"
                                title={`${r.file}:${r.line}`}
                                onClick={() => uiStore.setSelectedNodeId(r.id)}
                              >
                                {r.label}
                              </button>
                              <Show when={metaKey && r.meta?.[metaKey!]}>
                                <span class="text-[10px] text-amber-400">{r.meta![metaKey!]}</span>
                              </Show>
                            </li>
                          )}
                        </For>
                      </ul>
                    </div>
                  </Show>
                );
                return (
                  <div class="flex flex-col gap-2 border border-gray-800 rounded p-2">
                    <h3 class="text-[10px] uppercase tracking-wide text-orange-400">Variable flow</h3>
                    {section("Mutated by", vf().writers, "op")}
                    {section("Read by", vf().readers)}
                    {section("Captured by (closure)", vf().captured_by, "by")}
                    {section("Flows to", vf().flows_to, "mode")}
                  </div>
                );
              }}
            </Show>

            {/* Edges */}
            <Show when={(d().edges_from?.length ?? 0) > 0}>
              <div>
                <h3 class="text-[10px] uppercase tracking-wide text-gray-500 mb-1">
                  Outgoing ({d().edges_from.length})
                </h3>
                <ul class="flex flex-col gap-0.5">
                  <For each={d().edges_from}>{(e) => edgeRow(e, d().node.id, true)}</For>
                </ul>
              </div>
            </Show>
            <Show when={(d().edges_to?.length ?? 0) > 0}>
              <div>
                <h3 class="text-[10px] uppercase tracking-wide text-gray-500 mb-1">
                  Incoming ({d().edges_to.length})
                </h3>
                <ul class="flex flex-col gap-0.5">
                  <For each={d().edges_to}>{(e) => edgeRow(e, d().node.id, false)}</For>
                </ul>
              </div>
            </Show>

            <Show when={!source()}>
              <button
                class="text-xs text-indigo-400 hover:text-indigo-300 self-start cursor-pointer"
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
