import { Component, Show, createSignal, onCleanup, onMount } from "solid-js";
import { graphStore } from "../stores/graph";
import { uiStore, Layout } from "../stores/ui";
import { fetchMermaid, downloadText, downloadBlob, exportFilename, MermaidLevel } from "../lib/export";
import { getCy } from "./Graph";

const LAYOUTS: Layout[] = ["dagre", "fcose", "circle", "grid"];

const Toolbar: Component = () => {
  const [exportOpen, setExportOpen] = createSignal(false);
  const [exporting, setExporting] = createSignal(false);
  let menuRef: HTMLDivElement | undefined;

  const closeOnOutsideClick = (e: MouseEvent) => {
    if (menuRef && !menuRef.contains(e.target as Node)) setExportOpen(false);
  };
  onMount(() => document.addEventListener("click", closeOnOutsideClick));
  onCleanup(() => document.removeEventListener("click", closeOnOutsideClick));

  const traceScope = () => {
    const root = graphStore.traceRoot();
    return root
      ? { root, direction: graphStore.traceDirection(), depth: graphStore.traceDepth() }
      : null;
  };

  async function exportMermaid(level: MermaidLevel) {
    setExporting(true);
    try {
      const text = await fetchMermaid(level, traceScope());
      downloadText(exportFilename("mermaid", level), text);
    } catch (e) {
      uiStore.setNotification(`Export failed: ${e}`);
    } finally {
      setExporting(false);
      setExportOpen(false);
    }
  }

  function exportSVG() {
    const cy = getCy();
    if (!cy) return;
    try {
      const svgText = (cy as any).svg({ full: true, scale: 1.5, bg: "#030712" });
      downloadText(exportFilename("svg"), svgText, "image/svg+xml");
    } catch (e) {
      uiStore.setNotification(`SVG export failed: ${e}`);
    }
    setExportOpen(false);
  }

  function exportPNG() {
    const cy = getCy();
    if (!cy) return;
    try {
      const blob = cy.png({ full: true, scale: 2, bg: "#030712", output: "blob" }) as unknown as Blob;
      downloadBlob(exportFilename("png"), blob);
    } catch (e) {
      uiStore.setNotification(`PNG export failed: ${e}`);
    }
    setExportOpen(false);
  }

  const btn = (active: boolean) =>
    `px-2 py-1 rounded text-xs cursor-pointer ${
      active ? "bg-indigo-600 text-white" : "bg-gray-800 text-gray-300 hover:bg-gray-700"
    }`;

  return (
    <header class="flex items-center gap-4 border-b border-gray-800 px-4 py-2 shrink-0">
      <h1 class="text-base font-bold tracking-tight text-indigo-400">polyflow</h1>

      {/* View altitude */}
      <div class="flex items-center gap-1">
        <span class="text-[10px] uppercase tracking-wide text-gray-500 mr-1">View</span>
        <button class={btn(uiStore.viewMode() === "indepth")} onClick={() => uiStore.setViewMode("indepth")}>
          In-depth
        </button>
        <button class={btn(uiStore.viewMode() === "highlevel")} onClick={() => uiStore.setViewMode("highlevel")}>
          High-level
        </button>
      </div>

      {/* Layout */}
      <div class="flex items-center gap-1">
        <span class="text-[10px] uppercase tracking-wide text-gray-500 mr-1">Layout</span>
        {LAYOUTS.map((l) => (
          <button class={btn(uiStore.layout() === l)} onClick={() => uiStore.setLayout(l)}>
            {l}
          </button>
        ))}
      </div>

      <button
        class="px-2 py-1 rounded text-xs bg-gray-800 text-gray-300 hover:bg-gray-700 cursor-pointer"
        onClick={() => getCy()?.fit(undefined, 30)}
        title="Fit graph to viewport"
      >
        Fit
      </button>

      {/* Export menu */}
      <div class="relative" ref={menuRef}>
        <button
          class="px-2 py-1 rounded text-xs bg-gray-800 text-gray-300 hover:bg-gray-700 cursor-pointer"
          onClick={() => setExportOpen(!exportOpen())}
          disabled={exporting()}
        >
          {exporting() ? "Exporting…" : "Export ▾"}
        </button>
        <Show when={exportOpen()}>
          <div class="absolute left-0 top-full mt-1 z-40 flex flex-col rounded border border-gray-700 bg-gray-900 shadow-lg min-w-44">
            <span class="px-3 pt-2 pb-1 text-[10px] uppercase tracking-wide text-gray-500">Diagram (Mermaid)</span>
            <button class="text-left px-3 py-1.5 text-xs text-gray-300 hover:bg-gray-800 cursor-pointer" onClick={() => exportMermaid("service")}>
              High-level (services)
            </button>
            <button class="text-left px-3 py-1.5 text-xs text-gray-300 hover:bg-gray-800 cursor-pointer" onClick={() => exportMermaid("function")}>
              In-depth (functions)
            </button>
            <span class="px-3 pt-2 pb-1 text-[10px] uppercase tracking-wide text-gray-500 border-t border-gray-800">Image (current view)</span>
            <button class="text-left px-3 py-1.5 text-xs text-gray-300 hover:bg-gray-800 cursor-pointer" onClick={exportSVG}>
              SVG
            </button>
            <button class="text-left px-3 py-1.5 text-xs text-gray-300 hover:bg-gray-800 cursor-pointer" onClick={exportPNG}>
              PNG
            </button>
          </div>
        </Show>
      </div>

      <div class="flex-1" />

      {/* Stats */}
      <Show when={graphStore.stats()}>
        {(s) => (
          <span class="text-xs text-gray-500">
            {s().nodes.toLocaleString()} nodes · {s().edges.toLocaleString()} edges
          </span>
        )}
      </Show>
    </header>
  );
};

export default Toolbar;
