import { For, Show, createMemo, onMount } from "solid-js";
import { setupStore, type SetupScope } from "../../stores/setup";

// Shared between SetupView's first-run wizard (an extra step after the
// initial index build) and SettingsView (an "Agents" section reachable any
// time afterward) — the same component and the same GET /api/setup/agents
// read means both surfaces show identical, live-from-disk state whether the
// last registration happened here or via `polyflow setup` in a terminal.
const SCOPES: { id: SetupScope; label: string }[] = [
  { id: "repo", label: "Repo (checked in)" },
  { id: "user", label: "User (this machine)" },
  { id: "global", label: "Global (all users)" },
];

export default function AgentSetupPanel() {
  onMount(() => setupStore.loadAgents());

  const scope = createMemo(() => setupStore.agentScope());

  function selectScope(s: SetupScope): void {
    void setupStore.loadAgents(s);
  }

  return (
    <div data-testid="agent-setup-panel" class="space-y-3">
      <div class="flex items-center gap-2 text-xs">
        <span class="text-neutral-400">Scope:</span>
        <div class="flex gap-1">
          <For each={SCOPES}>
            {(s) => (
              <button
                data-testid={`agent-setup-scope-${s.id}`}
                class={`px-2 py-0.5 rounded ${
                  scope() === s.id ? "bg-indigo-600 text-white" : "bg-neutral-800 text-neutral-400 hover:text-white"
                }`}
                onClick={() => selectScope(s.id)}
              >
                {s.label}
              </button>
            )}
          </For>
        </div>
      </div>

      <Show when={setupStore.agentsError()}>
        <div data-testid="agent-setup-list-error" class="text-red-400 text-xs">{setupStore.agentsError()}</div>
      </Show>

      <Show when={setupStore.agentsLoading() && setupStore.agents().length === 0}>
        <div class="text-neutral-500 text-xs">Loading agents…</div>
      </Show>

      <ul data-testid="agent-setup-list" class="space-y-2">
        <For each={setupStore.agents()}>
          {(agent) => {
            const applying = createMemo(() => setupStore.applyingAgent() === agent.name);
            const result = createMemo(() => setupStore.agentApplyResults()[agent.name]);
            const error = createMemo(() => setupStore.agentApplyErrors()[agent.name]);
            const effectiveScope = createMemo(() => (scope() === "global" && !agent.supports_global_scope ? "user" : scope()));

            return (
              <li data-testid="agent-setup-row" class="border border-neutral-800 rounded p-2 space-y-1.5">
                <div class="flex items-center gap-2">
                  <span data-testid="agent-setup-name" class="text-neutral-100 font-medium text-sm">
                    {agent.display_name}
                  </span>
                  <Show
                    when={!agent.mcp_status_error}
                    fallback={
                      <span data-testid="agent-setup-mcp-badge-unknown" class="text-[10px] px-1.5 py-0.5 rounded bg-neutral-800 text-neutral-400">
                        MCP status unknown
                      </span>
                    }
                  >
                    <span
                      data-testid="agent-setup-mcp-badge"
                      class={`text-[10px] px-1.5 py-0.5 rounded ${
                        agent.mcp_configured ? "bg-emerald-900/50 text-emerald-300" : "bg-neutral-800 text-neutral-400"
                      }`}
                    >
                      MCP {agent.mcp_configured ? "configured" : "not configured"}
                    </span>
                  </Show>
                  <Show when={agent.supports_hooks}>
                    <span
                      data-testid="agent-setup-hooks-badge"
                      class={`text-[10px] px-1.5 py-0.5 rounded ${
                        agent.hooks_configured ? "bg-emerald-900/50 text-emerald-300" : "bg-neutral-800 text-neutral-400"
                      }`}
                    >
                      Hooks {agent.hooks_configured ? "configured" : "not configured"}
                    </span>
                  </Show>
                  <button
                    data-testid="agent-setup-apply-button"
                    class="ml-auto px-2 py-0.5 rounded bg-indigo-700 hover:bg-indigo-600 disabled:opacity-50 text-white text-xs"
                    disabled={applying()}
                    onClick={() => void setupStore.applyAgent(agent.name, effectiveScope())}
                  >
                    {applying() ? "Configuring…" : "Configure"}
                  </button>
                </div>
                <div class="text-neutral-500 text-xs">{agent.description}</div>
                <Show when={effectiveScope() !== scope()}>
                  <div class="text-amber-400 text-[10px]">
                    {agent.display_name} has no global scope — falling back to user scope.
                  </div>
                </Show>
                <Show when={result()}>
                  <div data-testid="agent-setup-result" class="text-emerald-400 text-[10px] space-y-0.5">
                    <div>{result()!.mcp_result}</div>
                    <Show when={result()!.hooks_result}>
                      <div>{result()!.hooks_result}</div>
                    </Show>
                    <Show when={result()!.hooks_skipped}>
                      <div class="text-neutral-500">{result()!.hooks_skipped}</div>
                    </Show>
                  </div>
                </Show>
                <Show when={error()}>
                  <div data-testid="agent-setup-error" class="text-red-400 text-[10px]">{error()}</div>
                </Show>
              </li>
            );
          }}
        </For>
      </ul>
    </div>
  );
}
