package factpipe

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/internal/datalog"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// emit.go is FX.5 — emit + policy-as-data. Everything the Go link passes did
// after the derivation (edge construction, confidence, abstention, fan-out
// caps, the blind-spot ledger, dedup) is expressed as an `emit:` block in the
// framework YAML, so a framework needs no policy Go (the SA contract's L5
// "policy lives in Go" is replaced by "policy is data in the emit spec").
//
// One `emit:` entry binds one derived datalog relation to one graph edge
// family:
//
//	emit:
//	  - relation: gin_guard
//	    columns:  [Handler, Target, Expr, Depth]
//	    edge:  { from: {arg: Handler}, to: {arg: Target}, type: calls, label: middleware }
//	    meta:  { via: gin_middleware_use, middleware_expr: {arg: Expr} }
//	    confidence:
//	      - { when: "Depth == 0", value: certain }
//	      - { when: "Depth <= 2", value: inferred }
//	      - { else: heuristic }
//	    abstain:
//	      when: "count_distinct(Target by Handler, Expr) > 1"
//	    fan_out: { key: [Handler], max: 50, on_exceed: ledger_only }
//	    dedup:  [from, to, label]
//	    unresolved:
//	      when: "Target == null"
//	      ref:  { service: {arg: Service}, name: {arg: Name}, kind: middleware }

// frozenEdgeTypes is the closed, framework-agnostic edge vocabulary an emit
// spec may write (plan § FX.0). A spec naming anything else fails at compile,
// not at emit.
var frozenEdgeTypes = map[string]bool{
	"contains": true, "calls": true, "imports": true, "references": true,
	"renders": true, "defined_in": true, "component_impl": true, "http_call": true,
	"publishes": true, "subscribes": true, "job_enqueue": true, "job_perform": true,
	"navigates_to": true, "spawns": true, "dom_read": true, "dom_write": true,
	"dom_listen": true, "dom_contract": true, "backed_by": true,
	// reads: added for js_mobx (FX.8.9 2026-09-15) — a callback/computed/
	// observer-render reactive read of an observable|computed member.
	"reads": true,
	// queries/persists: added for gorm_tables (FX.8.23 2026-09-15) — a GORM
	// datastore call terminating at its schema-declared table.
	"queries": true, "persists": true,
}

// frozenNodeTypes is the closed node-type vocabulary a `mint:` block may
// construct (plan § FX.0's "generic-only" invariant, extended to nodes). Kept
// narrow on purpose — only the node types a proven mint consumer needs are
// listed here; add one only when a real pass needs it, not speculatively.
var frozenNodeTypes = map[string]bool{
	"file": true,
	// subscriber: added for pusher_consumer (FX.8 2026-09-14) — one
	// `subscriber` node per resolved ERB `pusher_config`/`render
	// "shared/pusher"` call site.
	"subscriber": true,
	// publisher: added for pusher_producer (FX.8.11 2026-09-15) — one
	// `publisher` node per resolved `notify_*`-forwarding call site.
	"publisher": true,
	// http_client: added for rails_helpers (FX.8.7 2026-09-15) — a
	// nav_link_rails_helper client node resolved in place (same id) or
	// fanned into candidate ids on a helper->route collision.
	"http_client": true,
	// variable: added for js_hoc (FX.8.8 2026-09-15) — SPA.1's synthetic
	// default-export component node, minted only when no existing node
	// already owns that file+label.
	"variable": true,
	// route: added for js_client_routes (FX.8.10 2026-09-15) — one node per
	// SPA client-side route-table entry.
	"route": true,
	// component: added for js_client_routes (FX.8.10 2026-09-15) — SPA.3's
	// external feature-registry component node, and `patch:`'s RT.1
	// variable->component retype-in-place Type override.
	"component": true,
	// element: added for templ_layer (FX.8.27 2026-09-15) — a DOM element
	// node minted from a templ component's dom_ids/dom_classes meta when no
	// HTML/JSX/stylesheet-sourced element node already claims that id/class.
	"element": true,
	// http_handler: added for rails_devise (FX.8.28 2026-09-15) — a
	// synthesized Devise default-route node with no in-repo controller
	// behind it (controller_module is always an explicit "").
	"http_handler": true,
}

// valueRef is an edge/meta/ref field source: a literal (a bare scalar or
// `{literal: X}`), `{arg: Column}` naming a column of the derived relation, or
// `{template: "..."}` — a string with `{Column}` placeholders substituted from
// the row (rails_filters' edge label is "<Kind> :<Cb>", which no single column
// holds and datalog cannot concatenate).
type valueRef struct {
	Arg      string
	Literal  string
	Template string
	isLit    bool
}

// UnmarshalYAML accepts a bare scalar (⇒ literal) or a mapping with `arg` /
// `literal` / `template`.
func (v *valueRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		v.Literal, v.isLit = n.Value, true
		return nil
	}
	var m struct {
		Arg      string  `yaml:"arg"`
		Literal  *string `yaml:"literal"`
		Template string  `yaml:"template"`
	}
	if err := n.Decode(&m); err != nil {
		return err
	}
	v.Arg = m.Arg
	v.Template = m.Template
	if m.Literal != nil {
		v.Literal, v.isLit = *m.Literal, true
	}
	return nil
}

func (v valueRef) set() bool { return v.Arg != "" || v.isLit || v.Template != "" }

var templatePlaceholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func (v valueRef) resolve(row map[string]string) string {
	if v.Arg != "" {
		return row[v.Arg]
	}
	if v.Template != "" {
		return templatePlaceholder.ReplaceAllStringFunc(v.Template, func(m string) string {
			return row[m[1:len(m)-1]]
		})
	}
	return v.Literal
}

type edgeSpec struct {
	From   valueRef `yaml:"from"`
	To     valueRef `yaml:"to"`
	Type   string   `yaml:"type"`
	Label  valueRef `yaml:"label"` // literal or {arg: Column} — rails_filters' label is "<kind> :<cb>"
	Method valueRef `yaml:"method"`
	Path   valueRef `yaml:"path"`
}

// mintSpec is a `mint:` block — the counterpart of `edgeSpec` for ensuring a
// graph.Node exists (constructing it if not), the terminal step several FX.8
// roster passes need and `edge:` alone cannot express. A row that resolves to
// the same `id` as an earlier row in the same Apply call mints only once
// (§ Apply's seenNode dedup) — a mint-per-row relation is expected to name the
// same node repeatedly (e.g. one row per edge landing on a shared file node).
type mintSpec struct {
	Node    string   `yaml:"node"` // graph.NodeType; validated against frozenNodeTypes
	ID      valueRef `yaml:"id"`
	Label   valueRef `yaml:"label"`
	Service valueRef `yaml:"service"`
	File    valueRef `yaml:"file"`
	Line    valueRef `yaml:"line"`
	// EndLine (FX.8.28) sets graph.Node.EndLine directly — rails_devise's
	// synthesized single-line route node has no real body span, so its
	// EndLine equals Line (ported behavior: the retired Go always set both
	// to the same value). Unset (the default) leaves EndLine at its
	// zero-value, unchanged from every mint consumer before this one.
	EndLine  valueRef            `yaml:"end_line"`
	Language valueRef            `yaml:"language"`
	Meta     map[string]valueRef `yaml:"meta"`
}

// replaceSpec is a `replace:` block — the FX.8.15 counterpart of `mint:` for a
// row that SUPERSEDES an existing node rather than gap-filling a missing one
// (ruby_job_inherit's candidate->subscriber promotion: the promoted node's ID
// embeds its new type, so it cannot keep the candidate's old ID). `old` names
// the row column carrying the ID being superseded; everything else builds the
// new node exactly like `mint:` (same frozen-type gate). The caller (stage 4's
// consumer, e.g. internal/indexer/link_passes.go) is responsible for the
// actual graph-level swap: EmitResult only reports which old ID maps to which
// new node, the same "gap-fill only" discipline `mint:` already has — this
// primitive never mutates anything itself.
type replaceSpec struct {
	Old      valueRef            `yaml:"old"`
	Node     string              `yaml:"node"`
	ID       valueRef            `yaml:"id"`
	Label    valueRef            `yaml:"label"`
	Service  valueRef            `yaml:"service"`
	File     valueRef            `yaml:"file"`
	Line     valueRef            `yaml:"line"`
	Language valueRef            `yaml:"language"`
	Meta     map[string]valueRef `yaml:"meta"`
}

func (r *replaceSpec) asMint() mintSpec {
	return mintSpec{
		Node: r.Node, ID: r.ID, Label: r.Label, Service: r.Service,
		File: r.File, Line: r.Line, Language: r.Language, Meta: r.Meta,
	}
}

// patchSpec is a `patch:` block — the FX.8.8 counterpart of `mint:` for a row
// that ADDS/OVERWRITES a handful of named meta keys on an EXISTING node
// without rebuilding it. `mint:`/`replace:` both construct a graph.Node from
// scratch out of only the keys the spec names — fine when the target node's
// whole meta set is known and narrow (rails_helpers' http_client nodes), but
// wrong for a pass (js_hoc) that stamps a handful of keys onto a
// function/class/method/variable node whose OTHER meta (stamped by earlier
// parse-time or link passes — js_variables.go's global_symbol/global_path,
// etc.) must survive untouched. `patch:` never sees or needs the untouched
// keys: EmitResult.Patches reports only (id, the named meta to overlay,
// optional component/end_line bump), same "report-only, caller applies"
// discipline `replace:`/`delete:` already established — the caller merges
// into the existing node's Meta map instead of replacing it.
//
// `node:` (optional, FX.8.10) additionally reports a Type override — RT.1's
// "a client_route's render target turns out to be a real component, retype
// the existing `variable` node to `component` in place" needs the node's
// OTHER fields (Label/File/Line/Language/Service, and every untouched Meta
// key) preserved exactly, which is `patch:`'s whole reason to exist; only
// the Type itself also needs to change, alongside the Meta overlay. Gated
// against frozenNodeTypes like `mint:`/`replace:` when set.
type patchSpec struct {
	ID        valueRef            `yaml:"id"`
	Node      string              `yaml:"node"`      // optional Type override; validated against frozenNodeTypes
	Component valueRef            `yaml:"component"` // "true" bumps meta["component"]="true" + end_line (if larger)
	EndLine   valueRef            `yaml:"end_line"`
	Meta      map[string]valueRef `yaml:"meta"`
	// DeleteMeta (FX.8.30) names existing meta keys to strip outright — a
	// literal list, the same key set for every row this relation resolves,
	// not a per-row value. config_baseurl's re-grade needs to REMOVE a
	// stale path_evidence/confidence_ceiling stamp, which Meta's overlay-
	// only merge (skip-if-empty) cannot express; the conditionality (which
	// rows regrade) lives in which relation a row lands in — a hub only
	// emits a fact to this relation for the rows that should clear.
	DeleteMeta []string `yaml:"delete_meta"`
}

// deleteSpec is a `delete:` block — the FX.8.15 counterpart that drops an
// existing node ID outright (ruby_job_inherit's un-promoted candidates: a
// class that never reaches an ActiveJob root is bookkeeping, not a node).
// `old` names the row column carrying the ID to delete. Like `replace:`, this
// primitive only reports the ID; the caller performs the actual delete + edge
// pruning.
type deleteSpec struct {
	Old valueRef `yaml:"old"`
}

// resolvedSpec is a `resolved:` block (FX.8.10) — an auxiliary output
// channel orthogonal to edge/mint/replace/delete/patch: it reports
// (service, name) pairs a row resolved, for the caller to retract matching
// Kind-tagged rows an EARLIER pass already ledgered. `js_client_routes`
// mints/joins a feature-registry key that a prior JSX-scan pass had already
// recorded as `jsx_component_unresolved`; the retired Go's own caller glue
// (internal/indexer/link_passes.go) already did exactly this retraction for
// two other passes (js_link's importedNames, js_globals'
// globallyResolved) — `resolved:` generalizes that shape into the emit spec
// instead of leaving it caller-side ad hoc.
type resolvedSpec struct {
	Service valueRef `yaml:"service"`
	Name    valueRef `yaml:"name"`
}

type confRule struct {
	When  string `yaml:"when"`
	Value string `yaml:"value"`
	Else  string `yaml:"else"`
}

type fanOutSpec struct {
	Key      []string `yaml:"key"`
	Max      int      `yaml:"max"`
	OnExceed string   `yaml:"on_exceed"` // ledger_only (default) | drop | unresolved
}

type unresolvedRefSpec struct {
	Service valueRef `yaml:"service"`
	Name    valueRef `yaml:"name"`
	File    valueRef `yaml:"file"`
	Line    valueRef `yaml:"line"`
	Kind    valueRef `yaml:"kind"` // literal or {arg: Column}
	// Targets (FX.8.27) carries a fan-out cap's suppressed-target sample
	// (templ_layer's dom_class_high_fanout — "file:line" entries, one per
	// line, truncated with a "+N more" marker) — graph.UnresolvedRef's own
	// Targets field, unused by every unresolved: block before this one.
	Targets valueRef `yaml:"targets"`
}

type unresolvedSpec struct {
	When string            `yaml:"when"`
	Ref  unresolvedRefSpec `yaml:"ref"`
}

// EmitSpec is one entry of a framework YAML's `emit:` list, as written.
type EmitSpec struct {
	Relation   string              `yaml:"relation"`
	Columns    []string            `yaml:"columns"`
	Rule       string              `yaml:"rule"` // SA.1 provenance Rule; defaults to Relation
	Edge       edgeSpec            `yaml:"edge"`
	Mint       *mintSpec           `yaml:"mint"`
	Replace    *replaceSpec        `yaml:"replace"`
	Delete     *deleteSpec         `yaml:"delete"`
	Patch      *patchSpec          `yaml:"patch"`
	Resolved   *resolvedSpec       `yaml:"resolved"`
	Meta       map[string]valueRef `yaml:"meta"`
	Confidence []confRule          `yaml:"confidence"`
	Abstain    struct {
		When string `yaml:"when"`
	} `yaml:"abstain"`
	FanOut     *fanOutSpec     `yaml:"fan_out"`
	Dedup      []string        `yaml:"dedup"`
	Unresolved *unresolvedSpec `yaml:"unresolved"`
}

// CompiledEmit is an EmitSpec with its `when:` predicates parsed and validated.
type CompiledEmit struct {
	spec       EmitSpec
	abstain    boolFn
	unresolved boolFn
	confidence []compiledConf
}

type compiledConf struct {
	pred  boolFn // nil ⇒ the `else` arm
	value string
}

// Relation returns the derived relation this spec consumes.
func (e *CompiledEmit) Relation() string { return e.spec.Relation }

// EmitResult is what Apply produces for one relation: graph edges, "never
// empty" unresolved refs (SA invariant), and ledger rows (fan-out drops). All
// three are sorted and deterministic.
type EmitResult struct {
	Edges      []graph.Edge
	Nodes      []graph.Node
	Unresolved []graph.UnresolvedRef
	Ledger     []graph.UnresolvedRef
	// Replaced maps an old node ID (a `replace:` block's `old`) to the new
	// node built for it — that new node is also in Nodes, the same "gap-fill"
	// shape `mint:` already has. The caller performs the actual swap.
	Replaced map[string]string
	// Deleted lists node IDs a `delete:` block named for removal outright.
	Deleted []string
	// Patches lists meta overlays a `patch:` block produced — the caller
	// merges Meta into the existing node's own Meta map (not a replace).
	Patches []NodePatch
	// Resolved lists "service\x00name" pairs a `resolved:` block reported —
	// the caller retracts matching ledger rows an earlier pass recorded.
	Resolved []string
}

// NodePatch is one `patch:` row resolved: overlay Meta onto the existing
// node named by ID (merge, not replace); if Component, also set
// meta["component"]="true" and, when EndLine exceeds the node's current
// EndLine, bump it and stamp meta["end_line"] — mirroring js_hoc's retired
// stampMeta closure exactly. Type is empty unless the spec's `node:` field
// set a Type override (FX.8.10's RT.1 retype-in-place).
type NodePatch struct {
	ID         string
	Type       graph.NodeType
	Meta       map[string]string
	Component  bool
	EndLine    int
	DeleteMeta []string
}

// CompileEmits parses a YAML document with a top-level `emit:` list.
func CompileEmits(src []byte) ([]CompiledEmit, error) {
	var doc struct {
		Emit []EmitSpec `yaml:"emit"`
	}
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	return CompileEmitSpecs(doc.Emit)
}

// CompileEmitSpecs compiles already-decoded specs (the pipeline path, where the
// YAML is unmarshalled once alongside the patterns).
func CompileEmitSpecs(specs []EmitSpec) ([]CompiledEmit, error) {
	out := make([]CompiledEmit, 0, len(specs))
	for _, s := range specs {
		ce, err := compileEmit(s)
		if err != nil {
			return nil, fmt.Errorf("emit %q: %w", s.Relation, err)
		}
		out = append(out, ce)
	}
	return out, nil
}

func compileEmit(s EmitSpec) (CompiledEmit, error) {
	var ce CompiledEmit
	if s.Relation == "" {
		return ce, fmt.Errorf("missing relation")
	}
	hasEdge := s.Edge.Type != ""
	if hasEdge {
		if !s.Edge.From.set() || !s.Edge.To.set() {
			return ce, fmt.Errorf("edge needs both from and to")
		}
		if !frozenEdgeTypes[s.Edge.Type] {
			return ce, fmt.Errorf("edge type %q is not in the frozen vocabulary", s.Edge.Type)
		}
	}
	if s.Mint != nil {
		if !frozenNodeTypes[s.Mint.Node] {
			return ce, fmt.Errorf("mint node type %q is not in the frozen vocabulary", s.Mint.Node)
		}
		if !s.Mint.ID.set() {
			return ce, fmt.Errorf("mint needs an id")
		}
	}
	if s.Replace != nil {
		if !s.Replace.Old.set() {
			return ce, fmt.Errorf("replace needs an old id")
		}
		if !frozenNodeTypes[s.Replace.Node] {
			return ce, fmt.Errorf("replace node type %q is not in the frozen vocabulary", s.Replace.Node)
		}
		if !s.Replace.ID.set() {
			return ce, fmt.Errorf("replace needs an id")
		}
	}
	if s.Delete != nil && !s.Delete.Old.set() {
		return ce, fmt.Errorf("delete needs an old id")
	}
	if s.Patch != nil {
		if !s.Patch.ID.set() {
			return ce, fmt.Errorf("patch needs an id")
		}
		if s.Patch.Node != "" && !frozenNodeTypes[s.Patch.Node] {
			return ce, fmt.Errorf("patch node type %q is not in the frozen vocabulary", s.Patch.Node)
		}
	}
	if s.Resolved != nil {
		if !s.Resolved.Service.set() || !s.Resolved.Name.set() {
			return ce, fmt.Errorf("resolved needs both service and name")
		}
	}
	nVerbs := 0
	for _, set := range []bool{s.Mint != nil, s.Replace != nil, s.Delete != nil, s.Patch != nil} {
		if set {
			nVerbs++
		}
	}
	if nVerbs > 1 {
		return ce, fmt.Errorf("emit allows at most one of mint/replace/delete/patch")
	}
	if !hasEdge && s.Mint == nil && s.Replace == nil && s.Delete == nil && s.Patch == nil && s.Resolved == nil {
		return ce, fmt.Errorf("emit needs an edge, a mint, a replace, a delete, a patch, a resolved, or a combination")
	}
	if s.Unresolved != nil && !s.Unresolved.Ref.Kind.set() {
		return ce, fmt.Errorf("unresolved.ref needs a kind")
	}

	var err error
	if ce.abstain, err = compilePredicate(s.Abstain.When); err != nil {
		return ce, fmt.Errorf("abstain: %w", err)
	}
	if s.Unresolved != nil {
		if ce.unresolved, err = compilePredicate(s.Unresolved.When); err != nil {
			return ce, fmt.Errorf("unresolved.when: %w", err)
		}
	}
	for i, c := range s.Confidence {
		if c.Else != "" {
			ce.confidence = append(ce.confidence, compiledConf{value: c.Else})
			continue
		}
		pred, err := compilePredicate(c.When)
		if err != nil {
			return ce, fmt.Errorf("confidence[%d]: %w", i, err)
		}
		ce.confidence = append(ce.confidence, compiledConf{pred: pred, value: c.Value})
	}
	ce.spec = s
	return ce, nil
}

// Apply turns one derived relation's tuples into edges / unresolved / ledger
// rows per the spec. prov (may be nil) supplies the SA.1 derivation rule per
// edge; the engine already tracked it, so this lookup is free.
func (e *CompiledEmit) Apply(rows []datalog.Tuple, prov *datalog.Provenance) EmitResult {
	cols := e.spec.Columns
	rmaps := make([]map[string]string, len(rows))
	for i, t := range rows {
		m := make(map[string]string, len(t)*2)
		for j, v := range t {
			if j < len(cols) {
				m[cols[j]] = v
			}
			m[strconv.Itoa(j)] = v
		}
		rmaps[i] = m
	}

	var res EmitResult
	seen := map[string]bool{}
	seenNode := map[string]bool{}
	hasEdge := e.spec.Edge.Type != ""

	type pending struct {
		edge   graph.Edge
		fanKey string
	}
	var pend []pending

	for i, row := range rmaps {
		if e.abstain != nil && e.abstain(row, rmaps) {
			continue
		}
		if e.unresolved != nil && e.unresolved(row, rmaps) {
			res.Unresolved = append(res.Unresolved, e.buildUnresolved(row))
			continue
		}
		if e.spec.Mint != nil {
			if n, ok := e.buildMint(row); ok && !seenNode[n.ID] {
				seenNode[n.ID] = true
				res.Nodes = append(res.Nodes, n)
			}
		}
		if e.spec.Replace != nil {
			if n, oldID, ok := e.buildReplace(row); ok {
				if !seenNode[n.ID] {
					seenNode[n.ID] = true
					res.Nodes = append(res.Nodes, n)
				}
				if res.Replaced == nil {
					res.Replaced = map[string]string{}
				}
				res.Replaced[oldID] = n.ID
			}
		}
		if e.spec.Delete != nil {
			if oldID := e.spec.Delete.Old.resolve(row); oldID != "" {
				res.Deleted = append(res.Deleted, oldID)
			}
		}
		if e.spec.Patch != nil {
			if p, ok := e.buildPatch(row); ok {
				res.Patches = append(res.Patches, p)
			}
		}
		if e.spec.Resolved != nil {
			svc, name := e.spec.Resolved.Service.resolve(row), e.spec.Resolved.Name.resolve(row)
			if svc != "" && name != "" {
				res.Resolved = append(res.Resolved, svc+"\x00"+name)
			}
		}
		if !hasEdge {
			continue
		}
		edge := e.buildEdge(row, rmaps, rows[i], prov)
		key := e.dedupKey(edge)
		if seen[key] {
			continue
		}
		seen[key] = true

		fk := ""
		if e.spec.FanOut != nil {
			parts := make([]string, len(e.spec.FanOut.Key))
			for k, c := range e.spec.FanOut.Key {
				parts[k] = row[c]
			}
			fk = strings.Join(parts, "\x00")
		}
		pend = append(pend, pending{edge, fk})
	}

	drop := map[int]bool{}
	if fo := e.spec.FanOut; fo != nil && fo.Max > 0 {
		groups := map[string][]int{}
		for idx, p := range pend {
			groups[p.fanKey] = append(groups[p.fanKey], idx)
		}
		for _, gk := range sortedKeys(groups) {
			members := groups[gk]
			if len(members) <= fo.Max {
				continue
			}
			var targets []string
			for _, mi := range members {
				drop[mi] = true
				targets = append(targets, pend[mi].edge.To)
			}
			sort.Strings(targets)
			targets = dedupStrings(targets)
			name := strings.ReplaceAll(gk, "\x00", "|")
			ledgerRow := graph.UnresolvedRef{
				Kind:    e.spec.Relation + "_fanout",
				Name:    name,
				Targets: strings.Join(targets, "\n"),
			}
			switch fo.OnExceed {
			case "drop":
				// nothing recorded
			case "unresolved":
				res.Unresolved = append(res.Unresolved, ledgerRow)
			default: // ledger_only
				res.Ledger = append(res.Ledger, ledgerRow)
			}
		}
	}

	for idx, p := range pend {
		if !drop[idx] {
			res.Edges = append(res.Edges, p.edge)
		}
	}

	sort.Slice(res.Edges, func(i, j int) bool { return res.Edges[i].ID < res.Edges[j].ID })
	sort.Slice(res.Nodes, func(i, j int) bool { return res.Nodes[i].ID < res.Nodes[j].ID })
	sortUnresolved(res.Unresolved)
	sortUnresolved(res.Ledger)
	if len(res.Deleted) > 0 {
		sort.Strings(res.Deleted)
		res.Deleted = dedupStrings(res.Deleted)
	}
	sort.Slice(res.Patches, func(i, j int) bool { return res.Patches[i].ID < res.Patches[j].ID })
	if len(res.Resolved) > 0 {
		sort.Strings(res.Resolved)
		res.Resolved = dedupStrings(res.Resolved)
	}
	return res
}

// buildPatch resolves one row into a NodePatch per the spec's `patch:`
// block. ok is false when the id resolves empty.
func (e *CompiledEmit) buildPatch(row map[string]string) (NodePatch, bool) {
	s := e.spec.Patch
	id := s.ID.resolve(row)
	if id == "" {
		return NodePatch{}, false
	}
	p := NodePatch{ID: id, Type: graph.NodeType(s.Node), Component: s.Component.resolve(row) == "true", DeleteMeta: s.DeleteMeta}
	if s.EndLine.set() {
		if v, err := strconv.Atoi(s.EndLine.resolve(row)); err == nil {
			p.EndLine = v
		}
	}
	if len(s.Meta) > 0 {
		meta := make(map[string]string, len(s.Meta))
		for _, k := range sortedKeys(s.Meta) {
			if v := s.Meta[k].resolve(row); v != "" {
				meta[k] = v
			}
		}
		if len(meta) > 0 {
			p.Meta = meta
		}
	}
	return p, true
}

// buildMint resolves one row into a graph.Node per the spec's `mint:` block.
// ok is false when the id resolves empty (the row's join left the mint's key
// column unset — nothing to construct).
func (e *CompiledEmit) buildMint(row map[string]string) (n graph.Node, ok bool) {
	return buildNode(e.spec.Mint, row)
}

// buildReplace resolves one row into a graph.Node per the spec's `replace:`
// block, plus the old ID it supersedes. ok is false when either the new id or
// the old id resolves empty.
func (e *CompiledEmit) buildReplace(row map[string]string) (n graph.Node, oldID string, ok bool) {
	oldID = e.spec.Replace.Old.resolve(row)
	if oldID == "" {
		return graph.Node{}, "", false
	}
	m := e.spec.Replace.asMint()
	n, ok = buildNode(&m, row)
	if !ok {
		return graph.Node{}, "", false
	}
	return n, oldID, true
}

func buildNode(m *mintSpec, row map[string]string) (n graph.Node, ok bool) {
	id := m.ID.resolve(row)
	if id == "" {
		return graph.Node{}, false
	}
	n = graph.Node{
		ID:       id,
		Type:     graph.NodeType(m.Node),
		Label:    m.Label.resolve(row),
		Service:  m.Service.resolve(row),
		File:     m.File.resolve(row),
		Language: m.Language.resolve(row),
	}
	if m.Line.set() {
		if v, err := strconv.Atoi(m.Line.resolve(row)); err == nil {
			n.Line = v
		}
	}
	if m.EndLine.set() {
		if v, err := strconv.Atoi(m.EndLine.resolve(row)); err == nil {
			n.EndLine = v
		}
	}
	if len(m.Meta) > 0 {
		meta := make(map[string]string, len(m.Meta))
		for _, k := range sortedKeys(m.Meta) {
			ref := m.Meta[k]
			// A literal (even "") is an explicit author decision to force the
			// key present — rails_devise's controller_module: "" (FX.8.28)
			// must always exist as an empty-string key, distinguishing "no
			// in-repo controller" (moduleKnown=true, "") from "not derivable
			// here" (key absent) downstream in rails_route_actions.dl. Only a
			// {arg:}/{template:} value that resolves empty is still treated
			// as absent — the existing "Go link passes build their meta maps
			// conditionally" discipline every other mint consumer relies on.
			if v := ref.resolve(row); v != "" || ref.isLit {
				meta[k] = v
			}
		}
		if len(meta) > 0 {
			n.Meta = meta
		}
	}
	return n, true
}

func (e *CompiledEmit) buildEdge(row map[string]string, all []map[string]string, tup datalog.Tuple, prov *datalog.Provenance) graph.Edge {
	s := e.spec
	from := s.Edge.From.resolve(row)
	to := s.Edge.To.resolve(row)
	label := s.Edge.Label.resolve(row)
	id := from + "->" + to + ":"
	if label != "" {
		id += label
	} else {
		id += s.Edge.Type
	}

	conf := e.confidenceFor(row, all)

	var meta map[string]string
	if len(s.Meta) > 0 {
		meta = make(map[string]string, len(s.Meta))
		for _, k := range sortedKeys(s.Meta) {
			// An empty value is an absent key: the Go link passes build their
			// meta maps conditionally (`if len(reg.only) > 0`), so a byte-diff
			// against them must not carry `only: ""`.
			if v := s.Meta[k].resolve(row); v != "" {
				meta[k] = v
			}
		}
		if len(meta) == 0 {
			meta = nil
		}
	}

	edge := graph.Edge{
		ID:         id,
		From:       from,
		To:         to,
		Type:       graph.EdgeType(s.Edge.Type),
		Label:      label,
		Confidence: conf,
		Meta:       meta,
	}
	if s.Edge.Method.set() {
		edge.Method = s.Edge.Method.resolve(row)
	}
	if s.Edge.Path.set() {
		edge.Path = s.Edge.Path.resolve(row)
	}

	rule := s.Rule
	if rule == "" {
		rule = s.Relation
	}
	if prov != nil {
		if ds, err := prov.Of(s.Relation, tup); err == nil && len(ds) > 0 && ds[0].Rule != "" {
			rule = ds[0].Rule
		}
	}
	edge.Sources = []graph.SourceRef{{
		Provider:   "static",
		Confidence: conf,
		Layer:      "L3",
		Rule:       rule,
	}}
	return edge
}

func (e *CompiledEmit) confidenceFor(row map[string]string, all []map[string]string) string {
	for _, c := range e.confidence {
		if c.pred == nil || c.pred(row, all) {
			return c.value
		}
	}
	return ""
}

func (e *CompiledEmit) buildUnresolved(row map[string]string) graph.UnresolvedRef {
	r := e.spec.Unresolved.Ref
	out := graph.UnresolvedRef{Kind: r.Kind.resolve(row)}
	if r.Service.set() {
		out.Service = r.Service.resolve(row)
	}
	if r.Name.set() {
		out.Name = r.Name.resolve(row)
	}
	if r.File.set() {
		out.File = r.File.resolve(row)
	}
	if r.Targets.set() {
		out.Targets = r.Targets.resolve(row)
	}
	if r.Line.set() {
		if n, err := strconv.Atoi(r.Line.resolve(row)); err == nil {
			out.Line = n
		}
	}
	return out
}

func (e *CompiledEmit) dedupKey(edge graph.Edge) string {
	fields := e.spec.Dedup
	if len(fields) == 0 {
		fields = []string{"from", "to", "type", "label"}
	}
	parts := make([]string, len(fields))
	for i, f := range fields {
		switch f {
		case "from":
			parts[i] = edge.From
		case "to":
			parts[i] = edge.To
		case "type":
			parts[i] = string(edge.Type)
		case "label":
			parts[i] = edge.Label
		case "method":
			parts[i] = edge.Method
		case "path":
			parts[i] = edge.Path
		}
	}
	return strings.Join(parts, "\x00")
}

func dedupStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

func sortUnresolved(u []graph.UnresolvedRef) {
	sort.Slice(u, func(i, j int) bool {
		if u[i].Kind != u[j].Kind {
			return u[i].Kind < u[j].Kind
		}
		if u[i].Name != u[j].Name {
			return u[i].Name < u[j].Name
		}
		if u[i].File != u[j].File {
			return u[i].File < u[j].File
		}
		return u[i].Line < u[j].Line
	})
}
