import { For, Show, createMemo, createResource, createSignal } from "solid-js";
import { apiFetchJSON } from "../../lib/apiFetch";
import MarkdownPreview from "../../lib/MarkdownPreview";
import { KEY_BINDINGS } from "../../interaction/keys";
import guideMd from "../../docs/guide.md?raw";
import conceptsMd from "../../docs/concepts.md?raw";
import parityMd from "../../docs/parity.md?raw";

// UO.4: in-UI docs — Setup / CLI reference / UI guide / Concepts. The CLI
// reference is generated server-side from the live cobra tree (GET
// /api/docs/cli, see internal/meta/clidocs.go + cmd/polyflow/clidocs.go) so
// it can never go stale; the shortcut sheet below reads interaction/keys.ts
// directly for the same reason.
type CLIFlag = { name: string; shorthand?: string; default?: string; usage?: string };
type CLICommand = { name: string; short?: string; long?: string; usage?: string; flags?: CLIFlag[]; subcommands?: CLICommand[] };

type Section = "setup" | "cli" | "guide" | "concepts" | "parity";

const SECTIONS: { id: Section; label: string }[] = [
  { id: "setup", label: "Setup" },
  { id: "cli", label: "CLI reference" },
  { id: "guide", label: "UI guide" },
  { id: "concepts", label: "Concepts" },
  { id: "parity", label: "CLI ↔ UI parity" },
];

function anchorId(path: string[]): string {
  return `cli-${path.join("-")}`;
}

// Flattens the command tree into (path, command) pairs for search — a
// nested command like `config service add` searches and links the same as
// a top-level one.
function flatten(cmds: CLICommand[], prefix: string[] = []): { path: string[]; cmd: CLICommand }[] {
  const out: { path: string[]; cmd: CLICommand }[] = [];
  for (const c of cmds) {
    const path = [...prefix, c.name];
    out.push({ path, cmd: c });
    if (c.subcommands?.length) out.push(...flatten(c.subcommands, path));
  }
  return out;
}

function CommandBlock(props: { path: string[]; cmd: CLICommand }) {
  return (
    <div data-testid="cli-command" id={anchorId(props.path)} class="border-b border-neutral-800 py-2">
      <div class="text-xs font-semibold text-white">
        polyflow {props.path.join(" ")}
      </div>
      <Show when={props.cmd.short}>
        <div class="text-xs text-neutral-400">{props.cmd.short}</div>
      </Show>
      <Show when={props.cmd.usage}>
        <pre class="text-[11px] text-neutral-500 mt-1">{props.cmd.usage}</pre>
      </Show>
      <Show when={props.cmd.flags?.length}>
        <ul class="mt-1 space-y-0.5">
          <For each={props.cmd.flags}>
            {(f) => (
              <li class="text-[11px] text-neutral-400">
                <span class="text-neutral-200">
                  --{f.name}
                  {f.shorthand ? `, -${f.shorthand}` : ""}
                </span>
                {f.usage ? ` — ${f.usage}` : ""}
                {f.default ? ` (default ${f.default})` : ""}
              </li>
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
}

function CLIReference() {
  const [docs] = createResource(async () => {
    const r = await apiFetchJSON<{ commands: CLICommand[] }>("/api/docs/cli", { silent: true });
    return r.commands ?? [];
  });
  const [q, setQ] = createSignal("");

  const flat = createMemo(() => flatten(docs() ?? []));
  const filtered = createMemo(() => {
    const query = q().trim().toLowerCase();
    if (!query) return flat();
    return flat().filter(
      ({ path, cmd }) =>
        path.join(" ").toLowerCase().includes(query) || (cmd.short ?? "").toLowerCase().includes(query),
    );
  });

  return (
    <div data-testid="docs-cli" class="flex h-full min-h-0">
      <div class="w-56 shrink-0 border-r border-neutral-800 p-2 overflow-y-auto">
        <input
          data-testid="docs-cli-search"
          class="w-full bg-neutral-800 rounded px-1.5 py-0.5 text-xs mb-2"
          placeholder="search commands…"
          value={q()}
          onInput={(e) => setQ(e.currentTarget.value)}
        />
        <ul class="space-y-0.5">
          <For each={filtered()}>
            {({ path, cmd }) => (
              <li>
                <a
                  data-testid="docs-cli-anchor-link"
                  href={`#${anchorId(path)}`}
                  class="text-xs text-indigo-300 hover:text-indigo-200 block truncate"
                  title={cmd.short}
                >
                  {path.join(" ")}
                </a>
              </li>
            )}
          </For>
        </ul>
      </div>
      <div class="flex-1 overflow-y-auto p-2 min-w-0">
        <Show when={docs.loading}>
          <div class="text-xs text-neutral-400">Loading…</div>
        </Show>
        <Show when={!docs.loading && flat().length === 0}>
          <div class="text-xs text-neutral-500">No CLI docs yet — start the server via `polyflow serve` to generate them.</div>
        </Show>
        <For each={flat()}>{({ path, cmd }) => <CommandBlock path={path} cmd={cmd} />}</For>
      </div>
    </div>
  );
}

const MCP_SNIPPET = "claude mcp add polyflow -- polyflow mcp";

const CONFIG_EXAMPLE = `version: 1

services:
  - name: api               # unique service id used everywhere
    path: ./api             # repo root or subdirectory
    language: go            # go | ruby | python | javascript | typescript
    frameworks: [chi]       # optional hint; usually auto-detected
    port: 8080               # optional; helps HTTP link inference

  - name: web
    path: ./web
    language: typescript

# Known cross-service dependencies (seed link inference; usually optional).
links:
  - from: web
    to: api
    via: http               # http | rabbitmq | grpc | graphql
    hint: "API_URL=http://localhost:8080"
    base_url: /api

search:
  embedder: static          # static (default, zero-setup) | sidecar | endpoint

settings:
  port: 9400                # web UI / API server port`;

function Setup() {
  const [copied, setCopied] = createSignal(false);
  function copyMcp() {
    void navigator.clipboard.writeText(MCP_SNIPPET);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }
  return (
    <div data-testid="docs-setup" class="p-3 text-xs text-neutral-300 space-y-4 overflow-y-auto h-full">
      <div>
        <div class="text-sm font-semibold text-white mb-1">1. Initialize</div>
        <p class="text-neutral-400 mb-1">In your project root (or a folder containing your services):</p>
        <pre class="bg-neutral-900 border border-neutral-800 rounded p-2 text-[11px]">polyflow init</pre>
        <p class="text-neutral-500 mt-1">Auto-discovers services and writes polyflow.yml.</p>
      </div>
      <div>
        <div class="text-sm font-semibold text-white mb-1">2. Index</div>
        <pre class="bg-neutral-900 border border-neutral-800 rounded p-2 text-[11px]">polyflow index</pre>
        <p class="text-neutral-500 mt-1">Builds the graph — incremental on subsequent runs. The Jobs tab shows progress.</p>
      </div>
      <div>
        <div class="text-sm font-semibold text-white mb-1">3. Serve</div>
        <pre class="bg-neutral-900 border border-neutral-800 rounded p-2 text-[11px]">polyflow serve</pre>
        <p class="text-neutral-500 mt-1">Opens this UI. Re-run `polyflow index` any time; the graph reloads live.</p>
      </div>
      <div>
        <div class="text-sm font-semibold text-white mb-1">4. Register with an agent (MCP)</div>
        <p class="text-neutral-400 mb-1">Wire the MCP server so an AI agent can query the graph directly:</p>
        <div class="flex items-center gap-2">
          <pre data-testid="docs-mcp-snippet" class="bg-neutral-900 border border-neutral-800 rounded p-2 text-[11px] flex-1">{MCP_SNIPPET}</pre>
          <button
            data-testid="docs-mcp-copy"
            class="px-2 py-0.5 rounded bg-neutral-700 hover:bg-neutral-600 text-neutral-200 shrink-0"
            onClick={copyMcp}
          >
            {copied() ? "Copied" : "Copy"}
          </button>
        </div>
        <p class="text-neutral-500 mt-1">Or run the interactive wizard: <code class="bg-neutral-800 px-1 rounded">polyflow setup</code>.</p>
      </div>
      <div>
        <div class="text-sm font-semibold text-white mb-1">polyflow.yml (annotated example)</div>
        <pre data-testid="docs-config-example" class="bg-neutral-900 border border-neutral-800 rounded p-2 text-[11px] overflow-x-auto whitespace-pre">{CONFIG_EXAMPLE}</pre>
        <p class="text-neutral-500 mt-1">Editable from this UI too — see the Config activity.</p>
      </div>
    </div>
  );
}

function UIGuide() {
  return (
    <div data-testid="docs-guide" class="p-3 text-xs text-neutral-300 space-y-4 overflow-y-auto h-full">
      <MarkdownPreview markdown={guideMd} testId="docs-guide-rendered" />
      <table data-testid="docs-shortcut-sheet" class="w-full text-left">
        <thead>
          <tr class="text-neutral-500">
            <th class="pr-4 py-1">Key</th>
            <th class="py-1">Does</th>
          </tr>
        </thead>
        <tbody>
          <For each={KEY_BINDINGS}>
            {(b) => (
              <tr data-testid="docs-shortcut-row" class="border-t border-neutral-800">
                <td class="pr-4 py-1 text-neutral-200 font-mono">{b.display}</td>
                <td class="py-1 text-neutral-400">{b.description}</td>
              </tr>
            )}
          </For>
        </tbody>
      </table>
    </div>
  );
}

function Concepts() {
  return (
    <div data-testid="docs-concepts" class="p-3 overflow-y-auto h-full">
      <MarkdownPreview markdown={conceptsMd} testId="docs-concepts-rendered" />
    </div>
  );
}

// UO.7's parity matrix (docs/plan-13-ui-ops.md) — pinned in
// web/src/docs/parity.md and CI-enforced by cmd/polyflow's
// TestParityMatrixCoversAllCommands (walks the live cobra tree, fails if a
// command has no row here), so this can't silently fall behind the CLI.
// Rendered as raw text rather than through MarkdownPreview: its table is
// outside parseMarkdownLite's deliberately narrow (heading/code/list/quote/
// paragraph) shape, and a monospace pipe-table reads fine as-is.
function Parity() {
  return (
    <div data-testid="docs-parity" class="p-3 overflow-y-auto h-full">
      <pre data-testid="docs-parity-rendered" class="text-[11px] text-neutral-300 whitespace-pre-wrap">{parityMd}</pre>
    </div>
  );
}

export default function DocsPanel() {
  const [section, setSection] = createSignal<Section>("setup");

  return (
    <div data-testid="docs-panel" class="flex h-full min-h-0">
      <nav class="w-36 shrink-0 border-r border-neutral-800 p-2 space-y-0.5">
        <For each={SECTIONS}>
          {(s) => (
            <button
              data-testid={`docs-nav-${s.id}`}
              class={`w-full text-left px-2 py-1 rounded text-xs ${
                section() === s.id ? "bg-neutral-700 text-white" : "text-neutral-400 hover:text-white hover:bg-neutral-800"
              }`}
              onClick={() => setSection(s.id)}
            >
              {s.label}
            </button>
          )}
        </For>
      </nav>
      <div class="flex-1 min-w-0 min-h-0">
        <Show when={section() === "setup"}>
          <Setup />
        </Show>
        <Show when={section() === "cli"}>
          <CLIReference />
        </Show>
        <Show when={section() === "guide"}>
          <UIGuide />
        </Show>
        <Show when={section() === "concepts"}>
          <Concepts />
        </Show>
        <Show when={section() === "parity"}>
          <Parity />
        </Show>
      </div>
    </div>
  );
}
