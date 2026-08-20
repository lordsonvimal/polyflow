import { createSignal } from "solid-js";

// The set of node ids currently rendered on the canvas for the active scope
// (despite the generic name, CanvasHost only ever publishes node ids here —
// see its "Publish the active scope's rendered node ids" effect). Read by
// anything that needs to know "is this graph element visible right now"
// without importing CanvasHost itself: the tree explorer's two-way sync
// (offers "open scope" instead of silently no-opping when a tree row's node
// isn't on canvas) and UF.4's "Add all matches" (unions these into the
// multi-selection).
const [ids, setIds] = createSignal<ReadonlySet<string>>(new Set());

// UF.5: collapsed file-group id -> its real member node ids, published
// alongside `ids` whenever budget-forced clustering (CanvasHost's
// autoCluster) folds a file into one synthetic `filegrp:` node. "Copy
// context" on a scope must send the backend real node ids — a `filegrp:`
// id has no graph node behind it — so `expand` swaps every clustered id in
// for its members before the UB.6 request is built.
const [clusters, setClusters] = createSignal<ReadonlyMap<string, string[]>>(new Map());

// Tier NV.7: count of noise-classified edges in the active scope currently
// hidden by FilterBar's Noise chip row (lib/filters.ts's
// countHiddenByNoise), published here so FilterBar can render the "Noise
// (N hidden)" badge without CanvasHost's raw pre-filter edge set.
const [noiseHidden, setNoiseHidden] = createSignal(0);

export const canvasElementsStore = {
  ids,
  setIds: (next: ReadonlySet<string>) => setIds(next),
  has: (id: string) => ids().has(id),
  setClusters: (next: ReadonlyMap<string, string[]>) => setClusters(next),
  noiseHidden,
  setNoiseHidden: (n: number) => setNoiseHidden(n),
  expand: (idList: readonly string[]): { ids: string[]; clusterCount: number } => {
    const clusterMap = clusters();
    const out = new Set<string>();
    let clusterCount = 0;
    for (const id of idList) {
      const members = clusterMap.get(id);
      if (members) {
        clusterCount++;
        for (const m of members) out.add(m);
      } else {
        out.add(id);
      }
    }
    return { ids: [...out], clusterCount };
  },
};
