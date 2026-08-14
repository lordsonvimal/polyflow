import { createSignal, createEffect, For, Show, onCleanup, onMount } from "solid-js";
import { paletteStore, type RecentItem } from "../../stores/palette";
import { commands as registeredCommands, type Command } from "../../commands/registry";
import { handleIntent } from "../../interaction/gestures";
import { scopeStore } from "../../stores/scope";
import { selectionStore } from "../../stores/selection";
import { treeStore } from "../../stores/tree";
import { parseQuery, parseNodeCard, type ParsedQuery } from "./query";
import { formatLocation, displayLabel } from "../../lib/location";
import { linkExplorerStore } from "../../stores/linkExplorer";

const DEBOUNCE_MS = 150;
const RESULT_LIMIT = 8;

type SymbolEntry = {
  id: string; label: string; type: string; service: string; file: string; line: number;
  endLine?: number;
  // "semantic" = the hit only matched by vector similarity, not a literal
  // token — surfaced as a confidence dot so a fuzzy match never looks as
  // certain as a lexical/exact one.
  retrieval?: string;
};
type FileEntry = { file: string; service: string };
type ServiceEntry = { name: string };
type Entry =
  | { group: "recent"; item: RecentItem }
  | { group: "symbol"; item: SymbolEntry }
  | { group: "file"; item: FileEntry }
  | { group: "service"; item: ServiceEntry }
  | { group: "command"; item: Command };

async function fetchSymbols(parsed: ParsedQuery): Promise<SymbolEntry[]> {
  if (!parsed.text) return [];
  const params = new URLSearchParams({ q: parsed.text, limit: String(RESULT_LIMIT) });
  if (parsed.chips.kind) params.set("kind", parsed.chips.kind);
  try {
    const r = await fetch(`/api/graph/search?${params}`);
    if (!r.ok) return [];
    const data = await r.json();
    let out: SymbolEntry[];
    if (Array.isArray(data)) {
      out = data.map((n: any) => ({
        id: n.id, label: n.label, type: n.type, service: n.service, file: n.file, line: n.line,
        endLine: n.end_line, retrieval: "lexical",
      }));
    } else {
      out = (data.nodes ?? []).map((hit: any) => {
        const card = parseNodeCard(hit.entity?.Text ?? "");
        return {
          id: hit.entity?.ID ?? hit.entity?.NodeID ?? "",
          label: card.label,
          type: card.type,
          service: card.service,
          file: hit.entity?.File ?? "",
          line: hit.entity?.Line ?? 0,
          retrieval: hit.retrieval,
        };
      });
    }
    if (parsed.chips.service) out = out.filter(s => s.service === parsed.chips.service);
    return out.slice(0, RESULT_LIMIT);
  } catch {
    return [];
  }
}

async function fetchFiles(parsed: ParsedQuery): Promise<FileEntry[]> {
  const params = new URLSearchParams({ limit: String(RESULT_LIMIT) });
  if (parsed.text) params.set("q", parsed.text);
  try {
    const r = await fetch(`/api/files?${params}`);
    if (!r.ok) return [];
    const data = await r.json();
    let out: FileEntry[] = (data.files ?? []).map((f: any) => ({ file: f.file, service: f.service }));
    if (parsed.chips.service) out = out.filter(f => f.service === parsed.chips.service);
    return out.slice(0, RESULT_LIMIT);
  } catch {
    return [];
  }
}

// Services aren't a search-indexed entity — /api/stack is the whole list, so
// this filters the already-cached treeStore.services() (loaded once, on
// Palette mount, below) synchronously rather than issuing a network request
// on every keystroke. Empty until that initial load resolves.
function fetchServices(parsed: ParsedQuery): ServiceEntry[] {
  const needle = parsed.text.toLowerCase();
  let out = treeStore.services().map(s => ({ name: s.name }));
  if (needle) out = out.filter(s => s.name.toLowerCase().includes(needle));
  if (parsed.chips.service) out = out.filter(s => s.name === parsed.chips.service);
  return out.slice(0, RESULT_LIMIT);
}

function matchCommands(parsed: ParsedQuery): Command[] {
  const needle = parsed.text.toLowerCase();
  const all = registeredCommands();
  if (!needle) return all.slice(0, RESULT_LIMIT);
  return all.filter(c => c.label.toLowerCase().includes(needle)).slice(0, RESULT_LIMIT);
}

// A symbol result's Enter/pick behavior (UN.2): land in its file scope with
// it selected and revealed in the tree, in one action — replaces the old
// generic neighborhood-drill. A symbol with no known file (synthetic node)
// falls back to the neighborhood drill rather than silently no-opping.
function openSymbol(id: string, service: string, file: string) {
  if (file) {
    scopeStore.push({ kind: "file", service, path: file });
    selectionStore.setSelection({ kind: "node", id });
    treeStore.reveal(id);
  } else {
    handleIntent({ type: "select", target: { kind: "node", id } });
    handleIntent({ type: "drill", target: { kind: "node", id } });
  }
}

// UF.8: "Explore links" — selects the symbol and leaves a one-shot request
// for DetailHost's LinkExplorer toggle, deliberately WITHOUT pushing a
// scope change (unlike openSymbol/pick), so the peek/browse stays
// non-committal until the user acts inside the panel itself.
function exploreLinks(id: string) {
  selectionStore.setSelection({ kind: "node", id });
  linkExplorerStore.request(id);
}

export default function Palette() {
  let inputEl: HTMLInputElement | undefined;
  const [query, setQuery] = createSignal("");
  const [symbols, setSymbols] = createSignal<SymbolEntry[]>([]);
  const [files, setFiles] = createSignal<FileEntry[]>([]);
  const [services, setServices] = createSignal<ServiceEntry[]>([]);
  const [cmds, setCmds] = createSignal<Command[]>([]);
  const [highlight, setHighlight] = createSignal(0);

  let debounceTimer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;
  onCleanup(() => clearTimeout(debounceTimer));

  // Loaded once and cached on the treeStore singleton (shared with the tree
  // explorer / FilterBar) — not re-fetched per keystroke.
  onMount(() => treeStore.loadServices());

  function runSearch(raw: string) {
    const parsed = parseQuery(raw);
    setCmds(matchCommands(parsed));
    setServices(parsed.chips.kind ? [] : fetchServices(parsed)); // a kind: chip means "symbols only"
    if (!parsed.text && !parsed.chips.kind && !parsed.chips.service) {
      setSymbols([]);
      setFiles([]);
      return;
    }
    const mySeq = ++seq;
    Promise.all([fetchSymbols(parsed), fetchFiles(parsed)]).then(([s, f]) => {
      if (mySeq !== seq) return; // a newer keystroke already superseded this request
      setSymbols(s);
      setFiles(f);
    });
  }

  function onInput(v: string) {
    setQuery(v);
    setHighlight(0);
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => runSearch(v), DEBOUNCE_MS);
  }

  function entries(): Entry[] {
    if (query().trim() === "") {
      return paletteStore.recent().map(item => ({ group: "recent", item }) as Entry);
    }
    return [
      ...symbols().map(item => ({ group: "symbol", item }) as Entry),
      ...files().map(item => ({ group: "file", item }) as Entry),
      ...services().map(item => ({ group: "service", item }) as Entry),
      ...cmds().map(item => ({ group: "command", item }) as Entry),
    ];
  }

  function pick(entry: Entry) {
    switch (entry.group) {
      case "symbol": {
        const s = entry.item;
        openSymbol(s.id, s.service, s.file);
        paletteStore.addRecent({ id: s.id, kind: "symbol", label: s.label, sub: `${s.service} · ${s.file}` });
        break;
      }
      case "file": {
        const f = entry.item;
        scopeStore.push({ kind: "file", service: f.service, path: f.file });
        paletteStore.addRecent({ id: `${f.service}:${f.file}`, kind: "file", label: f.file, sub: f.service });
        break;
      }
      case "service": {
        const sv = entry.item;
        scopeStore.push({ kind: "service", service: sv.name });
        paletteStore.addRecent({ id: sv.name, kind: "service", label: sv.name });
        break;
      }
      case "command": {
        const c = entry.item;
        c.run();
        paletteStore.addRecent({ id: c.id, kind: "command", label: c.label });
        break;
      }
      case "recent": {
        const r = entry.item;
        if (r.kind === "symbol") {
          const [service, file] = (r.sub ?? "").split(" · ");
          openSymbol(r.id, service ?? "", file ?? "");
        } else if (r.kind === "file") {
          const [service, ...rest] = r.id.split(":");
          scopeStore.push({ kind: "file", service, path: rest.join(":") });
        } else if (r.kind === "service") {
          scopeStore.push({ kind: "service", service: r.id });
        } else {
          registeredCommands().find(c => c.id === r.id)?.run();
        }
        paletteStore.addRecent(r);
        break;
      }
    }
    close();
  }

  function close() {
    paletteStore.close();
    setQuery("");
    setSymbols([]);
    setFiles([]);
    setServices([]);
    setHighlight(0);
  }

  function onKeyDown(e: KeyboardEvent) {
    const list = entries();
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      close();
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      e.stopPropagation();
      setHighlight(h => (list.length === 0 ? 0 : (h + 1) % list.length));
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      e.stopPropagation();
      setHighlight(h => (list.length === 0 ? 0 : (h - 1 + list.length) % list.length));
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      e.stopPropagation();
      const entry = list[highlight()];
      if (entry) pick(entry);
      return;
    }
  }

  createEffect(() => {
    if (paletteStore.isOpen()) {
      setHighlight(0);
      const pending = paletteStore.pendingQuery();
      if (pending !== undefined) {
        onInput(pending);
        paletteStore.clearPendingQuery();
      }
      queueMicrotask(() => inputEl?.focus());
    }
  });

  return (
    <Show when={paletteStore.isOpen()}>
      <div
        data-testid="palette-overlay"
        class="fixed inset-0 z-50 flex items-start justify-center pt-24 bg-black/50"
        onClick={() => close()}
      >
        <div
          class="w-full max-w-lg bg-neutral-900 border border-neutral-700 rounded-lg shadow-xl overflow-hidden"
          onClick={(e) => e.stopPropagation()}
        >
          <input
            ref={inputEl}
            data-testid="palette-input"
            class="w-full px-3 py-2 bg-transparent text-sm text-neutral-100 outline-none border-b border-neutral-800"
            placeholder="Search symbols, files, commands… (kind:route service:name)"
            value={query()}
            onInput={(e) => onInput(e.currentTarget.value)}
            onKeyDown={onKeyDown}
          />
          <div class="max-h-96 overflow-y-auto overflow-x-hidden text-sm">
            <Show when={query().trim() === ""}>
              <Group label="RECENT">
                <For each={paletteStore.recent()}>
                  {(r, i) => (
                    <Row active={highlight() === i()} onClick={() => pick({ group: "recent", item: r })}>
                      <span class="text-neutral-200 shrink-0">{displayLabel(r.label)}</span>
                      <Show when={r.sub}><span class="text-neutral-500 ml-2 text-xs truncate min-w-0">{r.sub}</span></Show>
                    </Row>
                  )}
                </For>
              </Group>
            </Show>
            <Show when={query().trim() !== ""}>
              <Group label="SYMBOLS">
                <For each={symbols()}>
                  {(s, i) => (
                    <Row active={highlight() === i()} onClick={() => pick({ group: "symbol", item: s })}>
                      <Show when={s.retrieval === "semantic"}>
                        <span
                          data-testid="confidence-dot"
                          title="inferred match (semantic similarity, not a literal match)"
                          class="w-1.5 h-1.5 rounded-full bg-amber-500 mr-1.5 shrink-0"
                        />
                      </Show>
                      <span class="text-neutral-200 shrink-0">{s.label}</span>
                      <span class="text-neutral-500 ml-2 text-xs truncate min-w-0">
                        {s.type} · {s.service} · {formatLocation(s.file, s.line, s.endLine)}
                      </span>
                      <button
                        data-testid="palette-explore-links"
                        class="ml-auto text-xs text-indigo-300 hover:text-indigo-200 opacity-0 group-hover:opacity-100 shrink-0 pl-2"
                        title="Explore links"
                        onClick={(e) => {
                          e.stopPropagation();
                          exploreLinks(s.id);
                          close();
                        }}
                      >
                        links
                      </button>
                    </Row>
                  )}
                </For>
              </Group>
              <Group label="FILES">
                <For each={files()}>
                  {(f, i) => (
                    <Row active={highlight() === symbols().length + i()} onClick={() => pick({ group: "file", item: f })}>
                      <span class="text-neutral-200 truncate min-w-0" title={f.file}>{displayLabel(f.file)}</span>
                      <span class="text-neutral-500 ml-2 text-xs shrink-0">{f.service}</span>
                    </Row>
                  )}
                </For>
              </Group>
              <Group label="SERVICES">
                <For each={services()}>
                  {(sv, i) => (
                    <Row
                      active={highlight() === symbols().length + files().length + i()}
                      onClick={() => pick({ group: "service", item: sv })}
                    >
                      <span class="text-neutral-200">{sv.name}</span>
                    </Row>
                  )}
                </For>
              </Group>
              <Group label="COMMANDS">
                <For each={cmds()}>
                  {(c, i) => (
                    <Row
                      active={highlight() === symbols().length + files().length + services().length + i()}
                      onClick={() => pick({ group: "command", item: c })}
                    >
                      <span class="text-neutral-200">{c.label}</span>
                    </Row>
                  )}
                </For>
              </Group>
            </Show>
          </div>
        </div>
      </div>
    </Show>
  );
}

function Group(props: { label: string; children: any }) {
  return (
    <div>
      <div class="px-3 pt-2 pb-1 text-[10px] font-semibold tracking-wide text-neutral-500">{props.label}</div>
      {props.children}
    </div>
  );
}

function Row(props: { active: boolean; onClick: () => void; children: any }) {
  return (
    <div
      class={`group px-3 py-1.5 cursor-pointer flex items-baseline min-w-0 ${props.active ? "bg-neutral-700" : "hover:bg-neutral-800"}`}
      onClick={props.onClick}
    >
      {props.children}
    </div>
  );
}
