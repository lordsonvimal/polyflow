// Package graph is a vendored, stable copy of the node/edge/unresolved-ref
// shapes a plugin author needs to build a Result — see
// docs/linker-plugin-architecture-plan.md's "Pinned Go surface" section.
//
// This is a deliberate copy of (a subset of) internal/graph/model.go, not an
// import of it: internal/graph is free to evolve polyflow's own on-disk and
// in-memory representation without breaking every plugin built against an
// older SDK. This package's shape only changes when sdk/linkplugin's
// protocol_version bumps.
package graph

// NodeType and EdgeType are plain strings, not a closed enum: a plugin is
// expected to mint new kinds core has never seen (e.g. "acmerpc_route"),
// exactly the way patterns/<lang>/*.yaml already does for pattern-driven
// node/edge kinds.
type NodeType = string

type EdgeType = string

// Node mirrors internal/graph/model.go's Node.
type Node struct {
	ID       string
	Type     NodeType
	Label    string
	Service  string
	File     string
	Line     int
	EndLine  int
	Language string
	Meta     map[string]string
}

// SourceRef mirrors internal/graph/model.go's SourceRef.
type SourceRef struct {
	Provider   string
	Confidence string
	Ref        string
	ObservedAt int64
	CodeFile   string
	CodeFunc   string
}

// Edge mirrors internal/graph/model.go's Edge.
type Edge struct {
	ID                  string
	From                string
	To                  string
	Type                EdgeType
	Label               string
	Confidence          string
	Method              string
	Path                string
	Meta                map[string]string
	Sources             []SourceRef
	VerificationState   string
	VerifiedGranularity string
}

// UnresolvedRef mirrors internal/graph/model.go's UnresolvedRef.
type UnresolvedRef struct {
	Service string
	File    string
	Line    int
	Name    string
	Kind    string
	Targets string
}
