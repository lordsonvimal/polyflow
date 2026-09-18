package valuegraphfacts

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/jsast"
	"github.com/lordsonvimal/polyflow/internal/valuegraph"
)

// Site is one candidate expression a caller wants resolved — the output of a
// tree-sitter query, not this package's business. Owner is the component/
// class label the site belongs to ("" for an intraprocedural caller with no
// CrossSource need, e.g. Tier UL).
type Site struct {
	File  string
	Line  int
	Expr  *sitter.Node
	Root  *sitter.Node
	Scope *sitter.Node // enclosing function; nil means "infer from Expr"
	Owner string
}

// Result is one Site's resolution.
type Result struct {
	Site  Site
	Value valuegraph.Value
}

// fileSource adapts jsast's parse cache to valuegraph.FileSource, optionally
// composed with a ComponentIndex to enable the spec's crossing rules — same
// composition internal/linker/valuegraph_adapter.go's jsEngineFileSource/
// jsPropFileSource split intraprocedural from crossing-capable callers with.
type fileSource struct {
	files []string
	cross *ComponentIndex // nil disables crossings, same as a caller with no CrossSource
}

func (s fileSource) Files() []string { return s.files }

func (s fileSource) Parse(file string) ([]byte, *sitter.Node, bool) {
	src, root, _, ok := jsast.Parse(file)
	if !ok || root == nil {
		return nil, nil, false
	}
	return src, root, true
}

func (s fileSource) OwnersIn(file string) []string {
	if s.cross == nil {
		return nil
	}
	return s.cross.OwnersIn(file)
}

func (s fileSource) FilesForOwner(owner string) []string {
	if s.cross == nil {
		return nil
	}
	return s.cross.FilesForOwner(owner)
}

// Resolve runs spec's engine over every site and returns one Result per
// site, in the same order — the exact per-site Engine.Resolve loop every
// crossing-aware pass in internal/linker currently hand-writes. cross may be
// nil (no crossing rules reachable, same as an intraprocedural caller).
func Resolve(spec *valuegraph.Spec, files []string, cross *ComponentIndex, sites []Site, opts valuegraph.Options) []Result {
	fs := fileSource{files: files, cross: cross}
	eng := valuegraph.New(spec, fs, opts)
	out := make([]Result, len(sites))
	for i, s := range sites {
		scope := s.Scope
		v := eng.Resolve(valuegraph.Query{
			File:  s.File,
			Root:  s.Root,
			Expr:  s.Expr,
			Scope: scope,
		})
		out[i] = Result{Site: s, Value: v}
	}
	return out
}
