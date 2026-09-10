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
