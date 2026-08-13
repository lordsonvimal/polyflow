import { Show, type JSX } from "solid-js";

export interface EmptyStateAction {
  label: string;
  onClick: () => void;
}

export default function EmptyState(props: {
  icon?: string;
  message: string;
  detail?: string;
  action?: EmptyStateAction;
  children?: JSX.Element;
}) {
  return (
    <div data-testid="empty-state" class="flex flex-col items-center justify-center gap-2 text-center p-6">
      <Show when={props.icon}>
        <span class="text-2xl opacity-60">{props.icon}</span>
      </Show>
      <p class="text-sm text-neutral-300">{props.message}</p>
      <Show when={props.detail}>
        <p class="text-xs text-neutral-500 max-w-sm">{props.detail}</p>
      </Show>
      <Show when={props.action}>
        {(a) => (
          <button
            data-testid="empty-state-action"
            class="mt-1 px-3 py-1 rounded bg-indigo-600 hover:bg-indigo-500 text-white text-xs"
            onClick={() => a().onClick()}
          >
            {a().label}
          </button>
        )}
      </Show>
      {props.children}
    </div>
  );
}

// Pinned empty states (US.5). Each names the action that gets a user
// unstuck; "Run index" degrades to CLI instructions until plan 13 ships
// the Jobs UI trigger.
export function NoIndexEmptyState(props: { onRunIndex?: () => void }) {
  return (
    <EmptyState
      icon="◆"
      message="No index yet"
      detail={
        props.onRunIndex
          ? "Run an index to populate the graph."
          : "Run `polyflow index` from the workspace root, then reload this view."
      }
      action={props.onRunIndex ? { label: "Run index", onClick: props.onRunIndex } : undefined}
    />
  );
}

export function EmptyScopeEmptyState(props: { onReset?: () => void }) {
  return (
    <EmptyState
      message="This scope has no elements"
      detail="Nothing in the graph matches this scope's filters."
      action={props.onReset ? { label: "Back to overview", onClick: props.onReset } : undefined}
    />
  );
}

export function NoSearchResultsEmptyState(props: { query?: string; onClear?: () => void }) {
  return (
    <EmptyState
      message={props.query ? `No results for "${props.query}"` : "No results"}
      detail="Try a different term, or widen the type filter."
      action={props.onClear ? { label: "Clear search", onClick: props.onClear } : undefined}
    />
  );
}
