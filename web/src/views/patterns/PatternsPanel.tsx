import { For, Show, createMemo, createSignal, onMount } from "solid-js";
import { patternsStore, type PatternInfo } from "../../stores/patterns";

// UO.7: Patterns viewer (Settings activity → "Patterns") — wraps `patterns
// list`/`patterns add` so the CLI and this panel read/write the same
// polyflow.yml `patterns:` list (internal/server/patterns.go).
function PatternRow(props: { pattern: PatternInfo; expanded: boolean; onToggle: () => void }) {
  return (
    <div data-testid="pattern-row" class="border-b border-neutral-800 text-xs">
      <button
        class="w-full flex items-center gap-2 px-2 py-1 text-left hover:bg-neutral-900"
        onClick={props.onToggle}
      >
        <span class="text-neutral-200 font-mono">{props.pattern.name}</span>
        <span class="text-neutral-500">{props.pattern.language}</span>
        <Show when={props.pattern.custom}>
          <span data-testid="pattern-custom-badge" class="text-[10px] px-1 rounded bg-indigo-900 text-indigo-200">custom</span>
        </Show>
        <Show when={props.pattern.package}>
          <span class="text-neutral-600">{props.pattern.package}{props.pattern.version_range ? ` ${props.pattern.version_range}` : ""}</span>
        </Show>
        <span class="ml-auto text-neutral-600 truncate max-w-[40%]">{props.pattern.source}</span>
      </button>
      <Show when={props.expanded}>
        <div data-testid="pattern-detail" class="px-3 pb-2 text-neutral-400 space-y-1">
          <Show when={props.pattern.node_type}><div>node_type: <span class="text-neutral-300">{props.pattern.node_type}</span></div></Show>
          <Show when={props.pattern.edge_type}><div>edge_type: <span class="text-neutral-300">{props.pattern.edge_type}</span></div></Show>
          <Show when={props.pattern.roles?.length}>
            <div>roles: <span class="text-neutral-300">{props.pattern.roles!.join(", ")}</span></div>
          </Show>
          <Show when={props.pattern.grammars?.length}>
            <div>grammars: <span class="text-neutral-300">{props.pattern.grammars!.join(", ")}</span></div>
          </Show>
          <div>source: <span class="text-neutral-300 break-all">{props.pattern.source}</span></div>
        </div>
      </Show>
    </div>
  );
}

function AddPatternForm(props: { onDone: () => void }) {
  const [name, setName] = createSignal("");
  const [content, setContent] = createSignal("language: go\npatterns:\n  - name: my_pattern\n    query: \"(...)\"\n    extract:\n      node_type: function\n");

  async function submit(e: Event) {
    e.preventDefault();
    const ok = await patternsStore.add(name(), content());
    if (ok) {
      setName("");
      props.onDone();
    }
  }

  return (
    <form data-testid="pattern-add-form" class="p-2 space-y-2 border-b border-neutral-800" onSubmit={submit}>
      <input
        data-testid="pattern-add-name"
        class="w-full bg-neutral-800 rounded px-1.5 py-0.5 text-xs"
        placeholder="filename, e.g. my_pattern.yaml"
        value={name()}
        onInput={(e) => setName(e.currentTarget.value)}
      />
      <textarea
        data-testid="pattern-add-content"
        class="w-full h-40 bg-neutral-900 border border-neutral-800 rounded p-2 text-[11px] font-mono"
        value={content()}
        onInput={(e) => setContent(e.currentTarget.value)}
      />
      <Show when={patternsStore.addError()}>
        <div data-testid="pattern-add-error" class="text-red-400 text-[11px] whitespace-pre-wrap">{patternsStore.addError()}</div>
      </Show>
      <div class="flex justify-end gap-2">
        <button
          type="button"
          class="px-2 py-1 rounded text-neutral-400 hover:text-white text-xs"
          onClick={props.onDone}
        >
          Cancel
        </button>
        <button
          data-testid="pattern-add-submit"
          type="submit"
          disabled={patternsStore.adding() || !name().trim()}
          class="px-2 py-1 rounded bg-indigo-700 hover:bg-indigo-600 disabled:opacity-50 text-white text-xs"
        >
          {patternsStore.adding() ? "Adding…" : "Add pattern"}
        </button>
      </div>
    </form>
  );
}

export default function PatternsPanel() {
  const [q, setQ] = createSignal("");
  const [lang, setLang] = createSignal("");
  const [expanded, setExpanded] = createSignal<string | null>(null);
  const [showAdd, setShowAdd] = createSignal(false);

  onMount(() => {
    patternsStore.load();
  });

  const languages = createMemo(() => {
    const set = new Set<string>();
    for (const p of patternsStore.patterns()) set.add(p.language);
    return [...set].sort();
  });

  const filtered = createMemo(() => {
    const query = q().trim().toLowerCase();
    const l = lang();
    return patternsStore.patterns().filter((p) => {
      if (l && p.language !== l) return false;
      if (query && !p.name.toLowerCase().includes(query) && !p.language.toLowerCase().includes(query)) return false;
      return true;
    });
  });

  return (
    <div data-testid="patterns-panel" class="flex flex-col h-full min-h-0">
      <div class="flex items-center gap-2 p-2 border-b border-neutral-800 shrink-0">
        <input
          data-testid="patterns-search"
          class="flex-1 bg-neutral-800 rounded px-1.5 py-0.5 text-xs"
          placeholder="search patterns…"
          value={q()}
          onInput={(e) => setQ(e.currentTarget.value)}
        />
        <select
          data-testid="patterns-language-filter"
          class="bg-neutral-800 rounded px-1.5 py-0.5 text-xs"
          value={lang()}
          onChange={(e) => setLang(e.currentTarget.value)}
        >
          <option value="">all languages</option>
          <For each={languages()}>{(l) => <option value={l}>{l}</option>}</For>
        </select>
        <button
          data-testid="patterns-add-toggle"
          class="px-2 py-1 rounded bg-neutral-700 hover:bg-neutral-600 text-xs text-white shrink-0"
          onClick={() => setShowAdd((v) => !v)}
        >
          {showAdd() ? "Close" : "Add pattern…"}
        </button>
      </div>
      <Show when={showAdd()}>
        <AddPatternForm onDone={() => setShowAdd(false)} />
      </Show>
      <div class="flex-1 min-h-0 overflow-y-auto">
        <Show when={patternsStore.loading()}>
          <div class="p-3 text-xs text-neutral-400">Loading…</div>
        </Show>
        <Show when={!patternsStore.loading() && filtered().length === 0}>
          <div class="p-3 text-xs text-neutral-500">No patterns match.</div>
        </Show>
        <For each={filtered()}>
          {(p) => (
            <PatternRow
              pattern={p}
              expanded={expanded() === `${p.source}:${p.name}`}
              onToggle={() => setExpanded((cur) => (cur === `${p.source}:${p.name}` ? null : `${p.source}:${p.name}`))}
            />
          )}
        </For>
      </div>
    </div>
  );
}
