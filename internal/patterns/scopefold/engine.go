package scopefold

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/factpipe"
)

// stacks is the threaded accumulator state: stack name -> segments. Values,
// never mutated in place — push returns a new stacks with one stack's slice
// extended, everything else shared by reference. This is the non-aliasing
// discipline internal/parser/ruby_route_paths.go's own appendSeg comment
// documents ("append on a shared backing array let two sibling scopes
// overwrite each other's segment") generalized from one stack to N.
type stacks map[string][]string

func newStacks(names []string) stacks {
	s := make(stacks, len(names))
	for _, n := range names {
		s[n] = nil
	}
	return s
}

func (s stacks) push(name, seg string) stacks {
	if seg == "" {
		return s
	}
	out := make(stacks, len(s))
	for k, v := range s {
		out[k] = v
	}
	cur := out[name]
	next := make([]string, len(cur)+1)
	copy(next, cur)
	next[len(cur)] = seg
	out[name] = next
	return out
}

// reset clears one stack to empty (distinct from push: a scope that resets a
// stack ends whatever claim its ancestors made on it, rather than adding to
// it — Rails' namespace/scope ending the enclosing resource's naming claim).
func (s stacks) reset(name string) stacks {
	if len(s[name]) == 0 {
		return s
	}
	out := make(stacks, len(s))
	for k, v := range s {
		out[k] = v
	}
	out[name] = nil
	return out
}

// Fold walks root once, threading g's declared stacks through nested scope
// matches (matches indexed by declaration line, the same byLine convention
// internal/parser/ruby_route_paths.go's own composeRailsRoutePaths already
// uses), and returns one Fact per leaf match (plus any expand-table rows).
//
// Reuses the caller's already-parsed tree and already-computed matches — no
// re-parse. The caller is responsible for producing root/src/matches exactly
// as it would for an ordinary patterns: pass; this package owns none of
// that.
func Fold(root *sitter.Node, matches []Match, g *Grammar) []factpipe.Fact {
	byLine := make(map[int]*Match, len(matches))
	for i := range matches {
		byLine[matches[i].Line] = &matches[i]
	}
	scopes := make(map[string]*ScopeSpec, len(g.Scopes))
	for i := range g.Scopes {
		scopes[g.Scopes[i].Match] = &g.Scopes[i]
	}
	leaves := make(map[string]*LeafSpec, len(g.Leaves))
	for i := range g.Leaves {
		leaves[g.Leaves[i].Match] = &g.Leaves[i]
	}
	callTypes := g.CallNodeTypes
	if len(callTypes) == 0 {
		callTypes = []string{"call"}
	}
	f := &folder{
		grammar:   g,
		byLine:    byLine,
		scopes:    scopes,
		leaves:    leaves,
		callTypes: callTypes,
	}
	f.walk(root, newStacks(g.Stacks))
	return f.out
}

type folder struct {
	grammar   *Grammar
	byLine    map[int]*Match
	scopes    map[string]*ScopeSpec
	leaves    map[string]*LeafSpec
	callTypes []string
	out       []factpipe.Fact
}

func (f *folder) isCallType(n *sitter.Node) bool {
	t := n.Type()
	for _, ct := range f.callTypes {
		if ct == t {
			return true
		}
	}
	return false
}

func (f *folder) walk(n *sitter.Node, st stacks) {
	if n == nil {
		return
	}
	if !f.isCallType(n) {
		f.walkChildren(n, st)
		return
	}

	line := int(n.StartPoint().Row) + 1
	m := f.byLine[line]
	if m == nil {
		f.walkChildren(n, st)
		return
	}

	if scope, ok := f.scopes[m.PatternName]; ok {
		f.foldScope(n, scope, m, st)
		return
	}
	if leaf, ok := f.leaves[m.PatternName]; ok {
		f.emit(leaf.Emit, m, st)
		f.walkChildren(n, st)
		return
	}
	f.walkChildren(n, st)
}

func (f *folder) walkChildren(n *sitter.Node, st stacks) {
	for i := 0; i < int(n.ChildCount()); i++ {
		f.walk(n.Child(i), st)
	}
}

func (f *folder) foldScope(n *sitter.Node, scope *ScopeSpec, m *Match, st stacks) {
	next := st
	for _, name := range scope.Resets {
		next = next.reset(name)
	}
	for stackName, cs := range scope.Contributes {
		for _, c := range cs {
			// Read against st (the parent's pre-scope state), never next —
			// a Contribution.Stack read must see what the enclosing scope
			// had, not this scope's own in-progress pushes, and reading a
			// frozen st also makes push order across different target
			// stacks irrelevant (map iteration order can't matter).
			val := contributionValue(c, m, st)
			if val == "" {
				// A contribution this match cannot supply adds nothing
				// rather than inventing a segment — the same "a scope whose
				// contribution cannot be read must contribute nothing, since
				// inventing a segment shifts every route beneath it"
				// discipline internal/parser/ruby_route_paths.go's
				// scopeSegments documents.
				continue
			}
			next = next.push(stackName, val)
		}
	}
	if scope.NestParam != nil {
		if val := contributionValue(*scope.NestParam, m, st); val != "" {
			next = next.push("path", val)
		}
	}
	if scope.Expand != "" {
		f.expand(scope.Expand, m, next)
	}
	if scope.Recurse == "" {
		return
	}
	body := n.ChildByFieldName(scope.Recurse)
	if body == nil {
		return
	}
	if inner := body.ChildByFieldName("body"); inner != nil {
		body = inner
	}
	f.walk(body, next)
}

func (f *folder) emit(spec EmitSpec, m *Match, st stacks) {
	args := make([]factpipe.Atom, 0, len(spec.Args))
	for _, a := range spec.Args {
		args = append(args, f.evalArg(a, m, st, nil))
	}
	f.out = append(f.out, factpipe.Fact{
		Pred: spec.Pred,
		Args: args,
		Origin: factpipe.Origin{
			Kind:    factpipe.OriginPrimitive,
			File:    m.File,
			Line:    m.Line,
			Pattern: spec.Pred,
		},
	})
}

// row is non-nil only while evaluating an ExpandTable's Emit — it lets
// EmitArg.Row read the current synthesized row's own Name/Method, the one
// source of truth an expand-table row has that no stack or capture carries.
func (f *folder) evalArg(a EmitArg, m *Match, st stacks, row *ExpandRow) factpipe.Atom {
	val := f.evalArgPrimary(a, m, st, row)
	if val.Value() == "" && a.Fallback != nil {
		return f.evalArg(*a.Fallback, m, st, row)
	}
	return val
}

func (f *folder) evalArgPrimary(a EmitArg, m *Match, st stacks, row *ExpandRow) factpipe.Atom {
	if a.Row != "" && row != nil {
		switch a.Row {
		case "name":
			return factpipe.Str(row.Name)
		case "method":
			return factpipe.Str(row.Method)
		}
	}
	if a.Stack != "" {
		segs := st[a.Stack]
		if a.AppendCapture != "" {
			if seg := applyVerb(a.AppendExtract, m.Captures[a.AppendCapture]); seg != "" {
				segs = append(append([]string{}, segs...), seg)
			}
		}
		switch a.Compose {
		case "join_segments":
			return factpipe.Str(joinSegments(segs))
		case "helper_name":
			return factpipe.Str(strings.Join(segs, "_"))
		case "top":
			// The last-pushed value, unjoined — for scalar state threaded as
			// a one-deep-per-frame stack (Rails' onScope: "member"/
			// "collection" pushed by those two scopes, read back by a nested
			// verb route that has no keyword of its own).
			if len(segs) == 0 {
				return factpipe.Str("")
			}
			return factpipe.Str(segs[len(segs)-1])
		}
		return factpipe.Str(joinSegments(segs))
	}
	if a.Capture != "" {
		return factpipe.Str(applyVerb(a.Extract, m.Captures[a.Capture]))
	}
	return factpipe.Str(a.Literal)
}

// expand runs one table-driven implicit-construct synthesis: every row not
// filtered out by the scope match's only:/except:-shaped captures composes
// its own fact via table.Emit, the same mechanism a leaf's own Emit uses,
// generalizing internal/parser/ruby_route_paths.go's emitRESTRoutes off a
// hardcoded Go table onto grammar data.
func (f *folder) expand(tableName string, m *Match, st stacks) {
	table, ok := f.grammar.ExpandTables[tableName]
	if !ok {
		return
	}
	memberSeg := table.MemberSegment
	if memberSeg == "" {
		memberSeg = "*"
	}
	only, hasOnly, except := filterSet(m, table.FilterKeywords)
	seen := map[string]bool{}
	for i := range table.Rows {
		row := table.Rows[i]
		if hasOnly && !only[row.Name] {
			continue
		}
		if except[row.Name] {
			continue
		}
		rowStacks := st
		if row.Member {
			rowStacks = rowStacks.push("path", memberSeg)
		}
		if row.Suffix != "" {
			rowStacks = rowStacks.push("path", row.Suffix)
		}
		key := row.Method + " " + joinSegments(rowStacks["path"])
		if seen[key] {
			continue
		}
		seen[key] = true

		args := make([]factpipe.Atom, 0, len(table.Emit.Args))
		for _, a := range table.Emit.Args {
			args = append(args, f.evalArg(a, m, rowStacks, &row))
		}
		f.out = append(f.out, factpipe.Fact{
			Pred: table.Emit.Pred,
			Args: args,
			Origin: factpipe.Origin{
				Kind:    factpipe.OriginPrimitive,
				File:    m.File,
				Line:    m.Line,
				Pattern: table.Emit.Pred,
			},
		})
	}
}

// contributionValue resolves a Contribution's value: Literal, else a Capture
// (through its verb pipeline), else another stack's current top, else its
// Fallback recursively — the first of these that is non-empty wins.
func contributionValue(c Contribution, m *Match, st stacks) string {
	if c.Literal != "" {
		return c.Literal
	}
	if c.Capture != "" {
		if v := applyVerb(c.Extract, m.Captures[c.Capture]); v != "" {
			return v
		}
	}
	if c.Stack != "" {
		if segs := st[c.Stack]; len(segs) > 0 {
			return segs[len(segs)-1]
		}
	}
	if c.Fallback != nil {
		return contributionValue(*c.Fallback, m, st)
	}
	return ""
}

// filterSet reads only:/except:-shaped keyword captures off a scope match:
// a comma- or space-separated list of possibly ":"-prefixed names. hasOnly
// distinguishes an absent only: from an explicitly empty one, the same
// "only: [] means generate nothing, not everything" distinction
// restActionFilters documents.
func filterSet(m *Match, keywords []string) (only map[string]bool, hasOnly bool, except map[string]bool) {
	only, except = map[string]bool{}, map[string]bool{}
	if len(keywords) == 0 {
		return only, false, except
	}
	if raw, present := m.Captures[keywords[0]]; present {
		hasOnly = true
		for _, name := range splitNames(raw) {
			only[name] = true
		}
	}
	if len(keywords) > 1 {
		if raw, present := m.Captures[keywords[1]]; present {
			for _, name := range splitNames(raw) {
				except[name] = true
			}
		}
	}
	return only, hasOnly, except
}

func splitNames(raw string) []string {
	raw = strings.NewReplacer("[", "", "]", "", "%i(", "", "%i[", "", ")", "").Replace(raw)
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' })
	out := make([]string, 0, len(fields))
	for _, fld := range fields {
		if s := segment(fld); s != "" {
			out = append(out, s)
		}
	}
	return out
}
