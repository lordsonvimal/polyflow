import { apiFetch } from "../../lib/apiFetch";
import { flowHighlightStore } from "../../stores/flowHighlight";

// UF.8: the `[`/`]` keyboard peek-walk — fetches just the single nearest
// upstream/downstream neighbor (limit=1) and highlights it with the same
// cheap flowHighlightStore classes LinkExplorer's row hover uses. Works
// independent of whether the LinkExplorer panel is open, since the
// keyboard shortcut is scoped to "the current selection", not the panel.
export async function peekTopLink(nodeId: string, direction: "upstream" | "downstream"): Promise<void> {
  const p = new URLSearchParams({ direction, depth: "1", limit: "1" });
  const r = await apiFetch(`/api/node/${encodeURIComponent(nodeId)}/links?${p}`, { silent: true });
  const body = (await r.json()) as { rows: { node_id: string }[] };
  const top = body.rows[0];
  if (!top) {
    flowHighlightStore.clear();
    return;
  }
  flowHighlightStore.set([nodeId, top.node_id]);
}
