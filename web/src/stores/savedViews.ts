// UO.5: named ViewState snapshots persisted server-side (ops.db `views`
// table) — the star button's "save this exact canvas" and the Explorer's
// "Saved Views" list. State is stored as the same base64 payload the URL
// hash uses (encodeViewState/decodeViewState from stores/scope.ts), so a
// saved view and a copied share link decode identically.
import { createSignal } from "solid-js";
import { apiFetch, apiFetchJSON, ApiError } from "../lib/apiFetch";
import { notificationsStore } from "./notifications";
import { scopeStore, encodeViewState, decodeViewState, type Scope } from "./scope";

export interface SavedView {
  id: number;
  name: string;
  state: string; // encodeViewState payload
  created_at: string;
}

const [views, setViews] = createSignal<SavedView[]>([]);
const [loading, setLoading] = createSignal(false);
const [error, setError] = createSignal<string | null>(null);

async function list(): Promise<void> {
  setLoading(true);
  setError(null);
  try {
    const resp = await apiFetchJSON<{ views: SavedView[] }>("/api/views", { silent: true });
    setViews(resp.views);
  } catch (err) {
    setError(err instanceof Error ? err.message : String(err));
  } finally {
    setLoading(false);
  }
}

async function save(name: string): Promise<SavedView | null> {
  const state = encodeViewState(scopeStore.viewState());
  try {
    const resp = await apiFetchJSON<{ view: SavedView }>("/api/views", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, state }),
    });
    setViews((prev) => [resp.view, ...prev]);
    notificationsStore.add({ id: `saved-view-${resp.view.id}`, kind: "success", message: `Saved view "${name}"` });
    return resp.view;
  } catch (err) {
    const message = err instanceof ApiError && err.status === 409
      ? `A saved view named "${name}" already exists`
      : err instanceof Error ? err.message : String(err);
    notificationsStore.add({ id: `saved-view-err-${Date.now()}`, kind: "error", message });
    return null;
  }
}

async function rename(id: number, name: string): Promise<void> {
  try {
    const resp = await apiFetchJSON<{ view: SavedView }>(`/api/views/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    setViews((prev) => prev.map((v) => (v.id === id ? resp.view : v)));
  } catch (err) {
    const message = err instanceof ApiError && err.status === 409
      ? `A saved view named "${name}" already exists`
      : err instanceof Error ? err.message : String(err);
    notificationsStore.add({ id: `rename-view-err-${Date.now()}`, kind: "error", message });
  }
}

async function remove(id: number): Promise<void> {
  try {
    await apiFetch(`/api/views/${id}`, { method: "DELETE" });
    setViews((prev) => prev.filter((v) => v.id !== id));
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    notificationsStore.add({ id: `delete-view-err-${Date.now()}`, kind: "error", message });
  }
}

// Extracts the one graph-node id a scope is anchored to, if any — used to
// probe /api/node/{id} before committing a saved view, so a node deleted by
// a reindex since the view was saved is caught up front rather than
// surfacing as a blank/erroring canvas after the fact.
function anchorNodeId(scope: Scope): string | null {
  switch (scope.kind) {
    case "neighborhood":
      return scope.nodeId;
    case "impact":
      return scope.target;
    case "group":
      return scope.nodeIds[0] ?? null;
    case "flow":
      switch (scope.flow.kind) {
        case "through":
          return scope.flow.nodeId;
        case "varflow":
          return scope.flow.nodeId;
        case "edgeset":
          return scope.flow.nodeId;
        case "path":
          return scope.flow.from;
        case "waypoints":
          return scope.flow.ids[0] ?? null;
        case "pins":
          return scope.flow.ids[0] ?? null;
        default:
          return null;
      }
    default:
      return null;
  }
}

// Decode + apply a saved view, with US.1's stale-id fallback: if the
// anchor node no longer exists (deleted by a reindex since the view was
// saved), reset to overview via the same path CanvasHost's own stale-id
// handling uses, instead of pushing a scope that will just 404.
async function apply(view: SavedView): Promise<void> {
  const decoded = decodeViewState(view.state);
  if (!decoded) {
    notificationsStore.add({ id: `apply-view-corrupt-${view.id}`, kind: "error", message: `Saved view "${view.name}" is corrupted and can't be applied` });
    return;
  }

  const top = decoded.state.stack[decoded.state.stack.length - 1];
  const anchor = top ? anchorNodeId(top) : null;
  if (anchor) {
    try {
      await apiFetch(`/api/node/${encodeURIComponent(anchor)}`, { silent: true });
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        scopeStore.handleStaleId();
        notificationsStore.add({
          id: `apply-view-stale-${view.id}`,
          kind: "info",
          message: `"${view.name}" points to nodes that no longer exist after reindex — view reset to overview.`,
        });
        return;
      }
      // Non-404 (network/5xx): fall through and apply anyway rather than
      // blocking on a probe failure unrelated to the view's validity.
    }
  }

  scopeStore.applyViewState(decoded.state);
}

export const savedViewsStore = {
  views,
  loading,
  error,
  list,
  save,
  rename,
  remove,
  apply,
  // Test-only: module-level singleton, matching healthStore/jobsStore.
  reset: () => {
    setViews([]);
    setLoading(false);
    setError(null);
  },
};
