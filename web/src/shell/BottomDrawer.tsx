import { For, Show, createMemo } from "solid-js";
import { drawerStore, type DrawerTab } from "../stores/drawer";
import { contextCopyStore, TOKEN_BUDGETS } from "../stores/contextCopy";
import { parseMarkdownLite } from "../lib/markdownLite";
import type { CopyMode } from "../views/context/copy";

const TABS: { id: DrawerTab; label: string }[] = [
  { id: "context", label: "Context" },
  { id: "unresolved", label: "Unresolved" },
];

function MarkdownPreview(props: { markdown: string }) {
  const blocks = createMemo(() => parseMarkdownLite(props.markdown));
  return (
    <div data-testid="context-preview-rendered" class="space-y-1.5">
      <For each={blocks()}>
        {(b) => {
          if (b.type === "heading") {
            const cls = b.level === 1 ? "text-sm font-semibold text-white" : b.level === 2 ? "text-xs font-semibold text-neutral-200 mt-2" : "text-xs font-medium text-neutral-300 mt-1";
            return <div class={cls}>{b.text}</div>;
          }
          if (b.type === "code") {
            return <pre class="bg-neutral-900 border border-neutral-800 rounded p-2 text-[11px] text-neutral-300 overflow-x-auto whitespace-pre">{b.text}</pre>;
          }
          if (b.type === "list") {
            return (
              <ul class="list-disc list-inside text-xs text-neutral-300">
                <For each={b.items}>{(item) => <li>{item}</li>}</For>
              </ul>
            );
          }
          if (b.type === "quote") {
            return <div class="text-xs text-amber-300 border-l-2 border-amber-700 pl-2">{b.text}</div>;
          }
          return <div class="text-xs text-neutral-400 whitespace-pre-wrap">{b.text}</div>;
        }}
      </For>
    </div>
  );
}

function ContextTab() {
  const result = contextCopyStore.result;
  const err = contextCopyStore.error;

  return (
    <div data-testid="context-tab" class="flex h-full text-xs">
      <div class="w-48 shrink-0 border-r border-neutral-800 p-2 space-y-3 overflow-y-auto">
        <div>
          <div class="text-neutral-500 mb-1">Mode</div>
          <div class="flex gap-1">
            <For each={["viewed", "expanded"] as CopyMode[]}>
              {(m) => (
                <button
                  data-testid={`context-mode-${m}`}
                  class={`px-2 py-0.5 rounded ${contextCopyStore.mode() === m ? "bg-indigo-600 text-white" : "bg-neutral-800 text-neutral-400 hover:text-white"}`}
                  onClick={() => contextCopyStore.setMode(m)}
                >
                  {m === "viewed" ? "Viewed" : "Expanded"}
                </button>
              )}
            </For>
          </div>
        </div>

        <Show when={contextCopyStore.mode() === "expanded"}>
          <div>
            <div class="text-neutral-500 mb-1">Depth: {contextCopyStore.depth()}</div>
            <input
              data-testid="context-depth"
              type="range"
              min="1"
              max="5"
              value={contextCopyStore.depth()}
              onInput={(e) => contextCopyStore.setDepth(Number(e.currentTarget.value))}
              class="w-full"
            />
          </div>
        </Show>

        <label class="flex items-center gap-1.5 cursor-pointer">
          <input
            data-testid="context-snippets"
            type="checkbox"
            checked={contextCopyStore.snippets()}
            onChange={(e) => contextCopyStore.setSnippets(e.currentTarget.checked)}
          />
          <span class="text-neutral-300">Snippets</span>
        </label>

        <div>
          <div class="text-neutral-500 mb-1">Token budget</div>
          <div class="flex flex-wrap gap-1">
            <For each={TOKEN_BUDGETS}>
              {(b) => (
                <button
                  data-testid={`context-budget-${b}`}
                  class={`px-2 py-0.5 rounded ${contextCopyStore.maxTokens() === b ? "bg-indigo-600 text-white" : "bg-neutral-800 text-neutral-400 hover:text-white"}`}
                  onClick={() => contextCopyStore.setMaxTokens(b)}
                >
                  {b >= 1000 ? `${b / 1000}k` : b}
                </button>
              )}
            </For>
            <input
              data-testid="context-budget-custom"
              type="number"
              min="1"
              class="w-16 bg-neutral-800 rounded px-1 text-neutral-200"
              placeholder="custom"
              onChange={(e) => {
                const v = Number(e.currentTarget.value);
                if (v > 0) contextCopyStore.setMaxTokens(v);
              }}
            />
          </div>
        </div>

        <Show when={contextCopyStore.recent().length > 0}>
          <div>
            <div class="text-neutral-500 mb-1">Recent</div>
            <ul class="space-y-0.5">
              <For each={contextCopyStore.recent()}>
                {(b) => (
                  <li>
                    <button
                      data-testid="context-recent-item"
                      class="text-left text-indigo-300 hover:text-indigo-200 truncate block w-full"
                      title={b.label}
                      onClick={() => contextCopyStore.reopen(b)}
                    >
                      {b.label}
                    </button>
                  </li>
                )}
              </For>
            </ul>
          </div>
        </Show>
      </div>

      <div class="flex-1 overflow-y-auto p-2 min-w-0">
        <Show when={contextCopyStore.loading()}>
          <div class="text-neutral-500">Building context…</div>
        </Show>

        <Show when={err()}>
          {(message) => (
            <div data-testid="context-error" class="text-red-400 space-y-2">
              <div class="whitespace-pre-wrap">{message()}</div>
              <button
                data-testid="context-refresh-view"
                class="px-2 py-0.5 rounded bg-neutral-700 hover:bg-neutral-600 text-neutral-200"
                onClick={contextCopyStore.refreshView}
              >
                Refresh view
              </button>
            </div>
          )}
        </Show>

        <Show when={!contextCopyStore.loading() && !err() && result()}>
          {(r) => (
            <div class="space-y-2">
              <div class="flex items-center gap-2 flex-wrap">
                <span data-testid="context-token-estimate" class="text-neutral-400">
                  ~{r().tokens_estimate.toLocaleString()} tokens
                </span>
                <Show when={contextCopyStore.requestNote()}>
                  <span class="text-neutral-500">{contextCopyStore.requestNote()}</span>
                </Show>
                <button
                  data-testid="context-copy-clipboard"
                  class="ml-auto px-2 py-0.5 rounded bg-indigo-600 hover:bg-indigo-500 text-white"
                  onClick={contextCopyStore.copyToClipboard}
                >
                  Copy
                </button>
                <button
                  data-testid="context-download"
                  class="px-2 py-0.5 rounded bg-neutral-700 hover:bg-neutral-600 text-neutral-200"
                  onClick={contextCopyStore.downloadMarkdown}
                >
                  Download .md
                </button>
                <button
                  data-testid="context-raw-toggle"
                  class="px-2 py-0.5 rounded bg-neutral-800 hover:bg-neutral-700 text-neutral-300"
                  onClick={() => contextCopyStore.setRawView(!contextCopyStore.rawView())}
                >
                  {contextCopyStore.rawView() ? "Rendered" : "Raw"}
                </button>
              </div>

              <Show when={r().truncated}>
                <div data-testid="context-truncated-warning" class="text-amber-400">
                  ⚠ Truncated at {contextCopyStore.maxTokens().toLocaleString()} tokens.
                  <Show when={r().omitted.length > 0}>
                    {" "}Omitted: {r().omitted.join(", ")}
                  </Show>
                </div>
              </Show>

              <Show when={contextCopyStore.rawView()} fallback={<MarkdownPreview markdown={r().markdown} />}>
                <pre data-testid="context-preview-raw" class="text-[11px] text-neutral-300 whitespace-pre-wrap">{r().markdown}</pre>
              </Show>
            </div>
          )}
        </Show>

        <Show when={!contextCopyStore.loading() && !err() && !result()}>
          <div class="text-neutral-600">No context copied yet — use "Copy context" on a node, edge, flow, group, or scope.</div>
        </Show>
      </div>
    </div>
  );
}

function UnresolvedTab() {
  return (
    <div class="p-2 text-xs text-neutral-400">
      <Show when={drawerStore.unresolvedFilter()} fallback={<div>Unresolved refs — implemented in plan 13.</div>}>
        {(f) => (
          <span data-testid="unresolved-filter-chip" class="text-amber-400">
            ⚠ Unresolved · {f().service} · {f().path || "/"}
          </span>
        )}
      </Show>
    </div>
  );
}

export default function BottomDrawer() {
  const open = drawerStore.open;
  const setOpen = drawerStore.setOpen;

  return (
    <div
      data-testid="bottom-drawer"
      class="shrink-0 border-t border-neutral-800 dark:border-neutral-700 bg-neutral-950 transition-all flex flex-col"
      style={{ height: open() ? "260px" : "28px" }}
    >
      <div class="flex items-center px-2 h-7 gap-2 text-xs text-neutral-500 shrink-0">
        <button onClick={() => setOpen(!open())} class="hover:text-white">
          {open() ? "▼" : "▲"} Drawer
        </button>
        <Show when={open()}>
          <For each={TABS}>
            {(tab) => (
              <button
                data-testid={`drawer-tab-${tab.id}`}
                class={drawerStore.activeTab() === tab.id ? "text-white" : "hover:text-white"}
                onClick={() => drawerStore.setActiveTab(tab.id)}
              >
                {tab.label}
              </button>
            )}
          </For>
        </Show>
        <Show when={open()}>
          <button onClick={() => setOpen(false)} class="ml-auto hover:text-white">× close</button>
        </Show>
      </div>
      <Show when={open()}>
        <div class="flex-1 overflow-hidden">
          <Show when={drawerStore.activeTab() === "context"}>
            <ContextTab />
          </Show>
          <Show when={drawerStore.activeTab() === "unresolved"}>
            <UnresolvedTab />
          </Show>
        </div>
      </Show>
    </div>
  );
}
