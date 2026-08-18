import { createSignal } from "solid-js";
import { apiFetchJSON } from "../lib/apiFetch";
import { connectionStore } from "./connection";
import { isContainerGroupNodeId } from "../lib/aggregate";

// Mirrors internal/graph/tree.go's TreeNode/TreeResult (GET /api/tree).
export interface ApiTreeNode {
  kind: string;
  name: string;
  path?: string;
  node_id?: string;
  line?: number;
  end_line?: number;
  children: ApiTreeNode[];
}

export interface ApiTreeResult {
  service: string;
  tree: ApiTreeNode[];
  counts: { folders: number; files: number; symbols: number };
}

// Mirrors internal/graph/stack.go's DependencyInfo.
export interface DependencyInfo {
  name: string;
  version: string;
  ecosystem: string;
}

// Mirrors internal/graph/stack.go's ServiceStack (GET /api/stack). deps/
// nodeCounts/edgeCounts were already in the server response (UN.1's own
// server-change budget was spent elsewhere in this plan) but UN.0 never
// read them off the JSON — StackPanel (UN.4) is the first caller.
export interface ServiceSummary {
  name: string;
  language: string;
  frameworks: string[];
  files: number;
  deps: DependencyInfo[];
  nodeCounts: Record<string, number>;
  edgeCounts: Record<string, number>;
}

// The raw /api/stack wire shape (snake_case, as internal/graph/stack.go's
// ServiceStack marshals it) — loadServices() maps this into ServiceSummary.
interface ApiServiceStack {
  name: string;
  language: string;
  frameworks: string[];
  files: number;
  deps?: DependencyInfo[];
  node_counts?: Record<string, number>;
  edge_counts?: Record<string, number>;
}

// Mirrors internal/graph/model.go's UnresolvedRef (GET /api/unresolved).
export interface UnresolvedRef {
  service: string;
  file: string;
  line: number;
  name: string;
  kind: string;
}

const UNRESOLVED_PAGE_LIMIT = 1000;
const UNRESOLVED_PAGE_CAP = 5; // guards against pathological services (5000 refs)

function rowKeyForFolder(service: string, path: string): string {
  return `svc:${service}:folder:${path}`;
}
function rowKeyForFile(service: string, path: string): string {
  return `svc:${service}:file:${path}`;
}
function rowKeyForSymbol(service: string, nodeId: string): string {
  return `svc:${service}:sym:${nodeId}`;
}
function rowKeyForService(service: string): string {
  return `svc:${service}`;
}

export function rowKeyFor(service: string, n: ApiTreeNode): string {
  if (n.kind === "folder" && n.path !== undefined) return rowKeyForFolder(service, n.path);
  if (n.kind === "file" && n.path !== undefined) return rowKeyForFile(service, n.path);
  return rowKeyForSymbol(service, n.node_id ?? `${n.kind}:${n.name}:${n.line ?? 0}`);
}

// Node IDs are built service:file:type:name:line across every parser
// (see internal/parser/templ.go's templNodeID doc comment) — service is
// always the first colon-delimited segment.
export function serviceOfNodeId(nodeId: string): string {
  return nodeId.split(":")[0] ?? "";
}

interface NodeLocation {
  rowKey: string;
  ancestorRowKeys: string[]; // root-to-parent, service key first
}

function buildIndex(service: string, tree: ApiTreeNode[]): Map<string, NodeLocation> {
  const index = new Map<string, NodeLocation>();
  const walk = (nodes: ApiTreeNode[], ancestors: string[]) => {
    for (const n of nodes) {
      const rowKey = rowKeyFor(service, n);
      if (n.node_id) index.set(n.node_id, { rowKey, ancestorRowKeys: ancestors });
      walk(n.children ?? [], [...ancestors, rowKey]);
    }
  };
  walk(tree, [rowKeyForService(service)]);
  return index;
}

export interface ServiceEntry {
  tree?: ApiTreeResult;
  index?: Map<string, NodeLocation>;
  unresolved?: UnresolvedRef[];
  loading: boolean;
  error?: string;
}

// One flattened, virtualization-ready tree row.
export interface Row {
  key: string;
  depth: number;
  kind: string; // "service" | "folder" | "file" | ApiTreeNode.kind | "__loading__" | "__error__"
  name: string;
  service: string;
  path?: string;
  nodeId?: string;
  line?: number;
  endLine?: number;
  hasChildren: boolean;
  // Containing file path — set on the file row itself and inherited by its
  // symbol descendants, which carry no Path of their own (see TreeNode).
  file?: string;
}

function flattenNodes(
  service: string,
  nodes: ApiTreeNode[],
  depth: number,
  exp: ReadonlySet<string>,
  parentFile?: string,
): Row[] {
  const out: Row[] = [];
  for (const n of nodes) {
    const key = rowKeyFor(service, n);
    const hasChildren = (n.children?.length ?? 0) > 0;
    const file = n.kind === "file" ? n.path : parentFile;
    out.push({
      key,
      depth,
      kind: n.kind,
      name: n.name,
      service,
      path: n.path,
      nodeId: n.node_id,
      line: n.line,
      endLine: n.end_line,
      hasChildren,
      file,
    });
    if (hasChildren && exp.has(key)) {
      out.push(...flattenNodes(service, n.children, depth + 1, exp, file));
    }
  }
  return out;
}

// Pure — flattens the visible (respecting `exp`) rows across every service.
// Kept side-effect-free so windowing/aggregation math is unit-testable
// without a DOM or the reactive store.
export function buildRows(
  svcList: ServiceSummary[],
  entryMap: Record<string, ServiceEntry>,
  exp: ReadonlySet<string>,
): Row[] {
  const out: Row[] = [];
  for (const svc of svcList) {
    const key = rowKeyForService(svc.name);
    out.push({ key, depth: 0, kind: "service", name: svc.name, service: svc.name, hasChildren: true });
    if (!exp.has(key)) continue;
    const entry = entryMap[svc.name];
    if (!entry || entry.loading) {
      out.push({ key: `${key}:loading`, depth: 1, kind: "__loading__", name: "", service: svc.name, hasChildren: false });
    } else if (entry.error) {
      out.push({ key: `${key}:error`, depth: 1, kind: "__error__", name: entry.error, service: svc.name, hasChildren: false });
    } else if (entry.tree) {
      out.push(...flattenNodes(svc.name, entry.tree.tree, 1, exp));
    }
  }
  return out;
}

const [services, setServices] = createSignal<ServiceSummary[]>([]);
const [servicesLoading, setServicesLoading] = createSignal(false);
const [servicesError, setServicesError] = createSignal<string | undefined>(undefined);
const [entries, setEntries] = createSignal<Record<string, ServiceEntry>>({});
const [expanded, setExpanded] = createSignal<Set<string>>(new Set());
const [highlightedKey, setHighlightedKey] = createSignal<string | undefined>(undefined);

function entryFor(service: string): ServiceEntry {
  return entries()[service] ?? { loading: false };
}

function patchEntry(service: string, patch: Partial<ServiceEntry>) {
  setEntries((prev) => ({ ...prev, [service]: { ...(prev[service] ?? { loading: false }), ...patch } }));
}

async function fetchUnresolved(service: string): Promise<UnresolvedRef[]> {
  const out: UnresolvedRef[] = [];
  for (let page = 1; page <= UNRESOLVED_PAGE_CAP; page++) {
    const params = new URLSearchParams({ service, page: String(page), limit: String(UNRESOLVED_PAGE_LIMIT) });
    const data = await apiFetchJSON<{ refs: UnresolvedRef[]; total: number }>(`/api/unresolved?${params}`);
    out.push(...data.refs);
    if (out.length >= data.total || data.refs.length === 0) break;
  }
  return out;
}

async function loadService(service: string, force = false): Promise<void> {
  const existing = entryFor(service);
  if (!force && (existing.tree || existing.loading)) return;
  patchEntry(service, { loading: true, error: undefined });
  try {
    const [tree, unresolved] = await Promise.all([
      apiFetchJSON<ApiTreeResult>(`/api/tree?service=${encodeURIComponent(service)}`),
      fetchUnresolved(service),
    ]);
    patchEntry(service, { tree, index: buildIndex(service, tree.tree), unresolved, loading: false });
  } catch (err) {
    patchEntry(service, { loading: false, error: err instanceof Error ? err.message : String(err) });
  }
}

async function loadServices(): Promise<void> {
  if (servicesLoading() || services().length > 0) return;
  setServicesLoading(true);
  setServicesError(undefined);
  try {
    const data = await apiFetchJSON<{ services: ApiServiceStack[] }>("/api/stack");
    setServices(
      (data.services ?? [])
        .filter((s) => s.name)
        .map((s) => ({
        name: s.name,
        language: s.language,
        frameworks: s.frameworks ?? [],
        files: s.files,
        deps: s.deps ?? [],
        nodeCounts: s.node_counts ?? {},
        edgeCounts: s.edge_counts ?? {},
      })),
    );
  } catch (err) {
    setServicesError(err instanceof Error ? err.message : String(err));
  } finally {
    setServicesLoading(false);
  }
}

function toggleExpand(key: string) {
  setExpanded((prev) => {
    const next = new Set(prev);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    return next;
  });
}

function expandKeys(keys: string[]) {
  setExpanded((prev) => new Set([...prev, ...keys]));
}

// Aggregates unresolved-ref counts under a folder/file path prefix
// (folder: any ref whose file starts with path + "/"; file: exact match).
function unresolvedCount(service: string, kind: "folder" | "file", path: string): number {
  const refs = entryFor(service).unresolved ?? [];
  if (kind === "file") return refs.filter((r) => r.file === path).length;
  const prefix = path === "" ? "" : path + "/";
  return refs.filter((r) => r.file.startsWith(prefix)).length;
}

// Auto-expands and highlights the tree row backing a canvas node selection.
// Loads the owning service's tree first if it isn't cached yet. No-ops
// (beyond the load) if the node has no tree representation.
async function reveal(nodeId: string): Promise<void> {
  // Folder/file/service group ids are synthesized client-side by the
  // container/overview scopes and have no backing graph node — their
  // first colon segment ("folder"/"file"/"service") isn't a real service
  // name, so treating it as one sends /api/tree and /api/unresolved
  // requests for a service that doesn't exist.
  if (isContainerGroupNodeId(nodeId)) return;
  // Real node ids are always `service:file:type:name:line` (5 colon-
  // delimited segments — see aggregate.ts). Synthetic bucket ids like the
  // contract engine's "unresolved"/"unresolved:<svc>" node (no per-file
  // location) fail this, and treating their first segment as a service
  // name sends a doomed /api/tree + /api/unresolved request for a service
  // that doesn't exist — and, since loadService's re-entry guard only
  // blocks while a request is in flight (not after it fails), every
  // subsequent reveal() of that same id refires the same failing request.
  if (nodeId.split(":").length < 5) return;
  const service = serviceOfNodeId(nodeId);
  if (!service) return;
  await loadService(service);
  const loc = entryFor(service).index?.get(nodeId);
  if (!loc) return;
  expandKeys(loc.ancestorRowKeys);
  setHighlightedKey(loc.rowKey);
}

let unsubscribe: (() => void) | undefined;

function start(): void {
  if (unsubscribe) return;
  unsubscribe = connectionStore.onEvent((ev) => {
    if (ev.type !== "graph_updated") return;
    // Cache until graph_updated: invalidate every loaded service so the
    // next expand/reveal re-fetches instead of showing stale structure.
    setEntries({});
  });
}

function stop(): void {
  unsubscribe?.();
  unsubscribe = undefined;
}

// Test-only: clears all module-singleton state between test cases.
function reset(): void {
  setServices([]);
  setServicesLoading(false);
  setServicesError(undefined);
  setEntries({});
  setExpanded(new Set());
  setHighlightedKey(undefined);
}

export const treeStore = {
  services,
  servicesLoading,
  servicesError,
  loadServices,
  entries,
  entryFor,
  loadService,
  expanded,
  toggleExpand,
  expandKeys,
  highlightedKey,
  setHighlightedKey,
  unresolvedCount,
  reveal,
  rowKeyForService,
  rowKeyForFolder,
  rowKeyForFile,
  rowKeyForSymbol,
  start,
  stop,
  reset,
};
