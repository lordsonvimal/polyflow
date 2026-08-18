import { For, createMemo } from "solid-js";
import { parseMarkdownLite } from "./markdownLite";

// Shared with BottomDrawer.tsx's context preview (UF.5) and the Docs
// activity's guide/concepts pages (UO.4) — one renderer for the one
// markdown shape parseMarkdownLite understands.
export default function MarkdownPreview(props: { markdown: string; testId?: string }) {
  const blocks = createMemo(() => parseMarkdownLite(props.markdown));
  return (
    <div data-testid={props.testId ?? "markdown-preview-rendered"} class="space-y-1.5">
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
