package graph

// Snapshot is the read-only view of the graph-so-far that the declarative
// framework pipeline (Tier FX, docs/declarative-framework-pipeline-plan.md)
// bridges into base relations. It is the language-semantic output — nodes,
// edges, resolved names, imports — produced before framework linking runs;
// FX.1's bridge (internal/factpipe.GraphFacts) marshals it against a frozen
// relation schema so a framework `.dl` rule can join its own extracted facts
// against real name/type resolution and the containment/class backbone.
//
// Nothing in FX mutates a Snapshot. Nodes and Edges alias the slices the
// indexer already holds; Imports and Resolved are filled by the caller from
// internal/deps and the semantic analyzers respectively (both may be nil, in
// which case the corresponding relations are simply empty).
type Snapshot struct {
	Nodes    []Node
	Edges    []Edge
	Imports  []SnapshotImport
	Resolved []ResolvedName

	// Files is the service's complete file list (repo-relative paths, matching
	// Node.File), including files that declare nothing and so have no node — an
	// all-`//= require` asset manifest, a Sass partial of only `$variables`. The
	// bridge asserts it as service_file(Path, Dir, Stem, Ext) so a path
	// resolution rule (the resolve_path family) can probe candidate filenames
	// against the real file set. May be nil, in which case service_file is empty.
	Files []string

	// ServicePath is the service's root directory on disk (absolute or
	// cwd-relative, matching workspace.Service.Path) — the config_value
	// primitive's only disk access point: it reads the checked-in .env / k8s /
	// tfvars files under it (internal/configsrc.Load). Unlike Files, which is
	// an in-memory candidate list, config values live in file *contents*, so
	// this primitive genuinely needs a directory to scan rather than a name to
	// probe. May be "", in which case config_value's primitive relations are
	// empty — a framework with no `config:` block never reads it.
	ServicePath string

	// Links (FX.8.31, added for the "hints" hub) is the workspace's
	// fleet-wide cross-service link config (workspace.WorkspaceConfig.Links,
	// converted verbatim field-for-field) — the one input, like ServicePath,
	// a HubProvider cannot derive from any node/file/service-directory it
	// already sees, because a link rule names TWO services (From/To) and
	// lives in the workspace root config, not under either service's own
	// checkout. graph cannot import workspace (workspace already imports
	// graph), hence the local LinkHint mirror type rather than
	// []workspace.Link directly. May be nil, in which case a hub that reads
	// it simply sees no rules.
	Links []LinkHint

	// Schema (added for the schema_url_link + js_prop_clients hub) mirrors
	// workspace.SchemaConfig field-for-field — the corroboration-gate
	// thresholds and declared-asset globs for Tier MS's endpoint-declaring
	// data-asset discovery, which a HubProvider cannot derive from any
	// node/file it sees. Same graph-cannot-import-workspace reasoning as
	// Links. Zero value means every threshold falls back to its tested
	// default (see SchemaConfig).
	Schema SchemaConfig
}

// SchemaConfig mirrors workspace.SchemaConfig's fields — see Snapshot.Schema.
type SchemaConfig struct {
	Assets                  []string
	MinCorroboratedPaths    int
	MinCorroboratedRatio    float64
	MinEntityDiscrimination float64
	Disable                 bool
}

// LinkHint mirrors workspace.Link's matching fields (From/To/BaseURL/Hint) —
// see Snapshot.Links.
type LinkHint struct {
	From    string
	To      string
	BaseURL string
	Hint    string
}

// SnapshotImport is one `import(File, Package)` base fact: a source file and a
// third-party package it pulls in (internal/deps output, dependency-name
// normalized). Used by framework `.dl` rules that gate a derivation on a
// package being present in the importing file.
type SnapshotImport struct {
	File    string
	Package string
}

// ResolvedName is one `resolved(Site, Name, Target, Depth)` base fact: the
// semantic layer's name resolution for a call or reference site. Site is the
// node ID of the referencing construct, Name the identifier as written, Target
// the resolved node ID ("" when the name does not resolve to an indexed node),
// and Depth the lexical distance walked to reach the binding (0 = same scope,
// higher = further out a scope/superclass chain). Depth is what FX.5 grades an
// edge's confidence off — it replaces rails_filters' resolveSuper "first hit"
// and gin_middleware's SSA-edge lookup with a fact the rule reads directly.
type ResolvedName struct {
	Site   string
	Name   string
	Target string
	Depth  int
}
