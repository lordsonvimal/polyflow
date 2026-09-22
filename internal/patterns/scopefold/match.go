package scopefold

// Match is the subset of a pattern match this package needs: which pattern
// fired, where, and its captured text.
//
// Deliberately not internal/patterns.MatchResult: internal/patterns will
// eventually need to import this package (to wire a framework's
// scope_fold: YAML block to Fold, the way it already wires patterns:/hub:/
// emit:), and this package must never import internal/patterns back, or
// that becomes a cycle the day that wiring lands. A thin adapter in
// internal/patterns converts MatchResult -> Match at the call site instead
// — the same "convert at the boundary" shape railsinflect/railsview already
// use to stay dependency-free leaves under both internal/parser and
// internal/linker.
type Match struct {
	PatternName string
	File        string
	Line        int
	// Captures holds each named capture's raw matched source text, exactly
	// as internal/patterns.MatchResult.Captures does (e.g. a Ruby
	// simple_symbol capture reads ":foo", a string capture reads "\"foo\"" —
	// the segment verb strips both forms).
	Captures map[string]string
}
