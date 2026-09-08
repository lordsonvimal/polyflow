package linker

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// maxModelInheritHops bounds the inherits walk from a model class up to a
// known ActiveRecord root. Cedar's deepest real chain is 3
// (Standard -> VersionedRecord -> ApplicationRecord); 4 leaves headroom
// without letting an unrelated deep hierarchy drift into the table graph.
const maxModelInheritHops = 4

// activeRecordRootNames terminate the inherits walk. A class reaching one of
// these *is* an ActiveRecord model, however many project base classes sit in
// between. ApplicationRecord is the Rails generator's own base and is
// in-repo; ActiveRecord::Base is not, but a class naming it directly still
// gets an `inherits` edge to a class node of that name from
// LinkRubyTypeRelations' unresolved-reference handling, and is also matched
// by the direct-superclass check below.
var activeRecordRootNames = map[string]bool{
	"ApplicationRecord":  true,
	"ActiveRecord::Base": true,
}

const (
	ledgerModelTableUnresolved = "rails_model_table_unresolved"
	ledgerTableUnowned         = "rails_table_unowned"
)

// railsModelDir is the conventional location of ActiveRecord models. The
// pass is scoped to it for the same reason AT.1's schema gate exists: `class
// Foo < Bar` anywhere else in a Rails app is overwhelmingly not a model, and
// a class that reaches ApplicationRecord always lives here.
const railsModelDir = "app/models/"

// LinkRailsModelTables resolves ActiveRecord model classes to the table nodes
// minted from db/schema.rb (AT.1) and emits one EdgeTypeBackedBy per resolved
// pair.
//
// Resolution order is explicit `self.table_name` first, then the naive
// convention *validated against the known table set* — never naive
// pluralization on its own. A model whose conventional name is not in the set
// ledgers as rails_model_table_unresolved rather than being guessed at, which
// is what keeps irregular plurals from minting edges to tables that do not
// exist. Classes marked `self.abstract_class = true` own no table and are
// skipped without a ledger entry.
//
// Must run AFTER the parser has emitted class nodes, inherits edges, and the
// schema.rb table nodes; it consumes all three.
func LinkRailsModelTables(
	nodes []graph.Node,
	edges []graph.Edge,
	serviceFiles map[string][]string,
) (out []graph.Edge, ledger []graph.UnresolvedRef) {
	ix := newModelTableIndex(nodes, edges)
	if len(ix.tablesByService) == 0 {
		// No Rails schema anywhere in the workspace: every service here is a
		// .sql or no-database service, and AT has nothing to say about it.
		return nil, nil
	}

	// Service order decides edge order, so it cannot come from a map.
	svcNames := make([]string, 0, len(serviceFiles))
	for svcName := range serviceFiles {
		svcNames = append(svcNames, svcName)
	}
	sort.Strings(svcNames)

	owned := map[string]bool{} // table node ID -> some model claims it
	for _, svcName := range svcNames {
		if ix.tablesByService[svcName] == nil {
			continue
		}
		files := append([]string{}, serviceFiles[svcName]...)
		sort.Strings(files)
		var models []modelClass
		for _, absFile := range files {
			if !isRubyFile(absFile) || !isRailsModelFile(absFile) {
				continue
			}
			models = append(models, ix.readFile(svcName, absFile)...)
		}
		e, l := ix.linkService(svcName, models, owned)
		out = append(out, e...)
		ledger = append(ledger, l...)
	}

	// A table no model claims. Join tables, database views and
	// schema_migrations/ar_internal_metadata legitimately land here: the row
	// is the graph saying "this table is declared and nothing in this repo
	// owns it", which is information, not a defect.
	for _, t := range ix.tableOrder {
		if owned[t.id] {
			continue
		}
		ledger = append(ledger, graph.UnresolvedRef{
			Service: t.service, File: t.file, Line: t.line,
			Name: t.name, Kind: ledgerTableUnowned,
		})
	}
	return out, ledger
}

// isRailsModelFile reports whether file sits under app/models/. Path
// separators are normalized so this holds on Windows-style paths too.
func isRailsModelFile(file string) bool {
	return strings.Contains(filepath.ToSlash(file), railsModelDir)
}

// tableAtLine is a schema-declared table node's identity, kept in declaration
// order so the unowned ledger is deterministic without sorting a map.
type tableAtLine struct {
	id, name, service, file string
	line                    int
}

// modelTableIndex is the read-only view of the graph this pass walks.
type modelTableIndex struct {
	// tablesByService maps service -> table name -> node ID. Service-scoped
	// on purpose: two services in one workspace commonly declare the same
	// table name, and a model must never be backed by another service's
	// storage.
	tablesByService map[string]map[string]string
	tableOrder      []tableAtLine

	// classesInFile maps service\x00relative-file -> class nodes in it,
	// ascending by line.
	classesInFile map[string][]classAtLine
	// classNameOf describes the class layer LinkRubyTypeRelations built.
	//
	// superclassIDs is the `inherits` adjacency restricted to real
	// superclasses (meta via=superclass) — NOT the mixin half. Both walks
	// here ask a question only a superclass can answer: "does this class
	// reach ActiveRecord" and "whose table does it share". Including a
	// concern makes a class neither a model nor an heir to a table, and
	// following mixin edges made two of cedar's PORO copiers and one
	// ActiveSupport::Concern module look like unresolved models.
	classNameOf   map[string]string
	superclassIDs map[string][]string
}

func newModelTableIndex(nodes []graph.Node, edges []graph.Edge) *modelTableIndex {
	ix := &modelTableIndex{
		tablesByService: map[string]map[string]string{},
		classesInFile:   map[string][]classAtLine{},
		classNameOf:     map[string]string{},
		superclassIDs:   map[string][]string{},
	}
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeTable:
			// Only schema-declared Rails tables. A synthetic table LinkTables
			// minted for an unrecognised SQL string, or a .sql CREATE TABLE
			// in a sibling service, is not something a Rails model convention
			// may be validated against.
			if n.Meta["source"] != "rails_schema" {
				continue
			}
			byName := ix.tablesByService[n.Service]
			if byName == nil {
				byName = map[string]string{}
				ix.tablesByService[n.Service] = byName
			}
			if _, dup := byName[n.Label]; dup {
				// Two create_table calls for one name in one service: the
				// first declaration wins and the second is not separately
				// ownable. Rails' own dumper cannot produce this.
				continue
			}
			byName[n.Label] = n.ID
			ix.tableOrder = append(ix.tableOrder, tableAtLine{
				id: n.ID, name: n.Label, service: n.Service, file: n.File, line: n.Line,
			})
		case graph.NodeTypeClass:
			ix.classNameOf[n.ID] = n.Label
			k := jobFileKey(n.Service, n.File)
			ix.classesInFile[k] = append(ix.classesInFile[k], classAtLine{id: n.ID, name: n.Label, line: n.Line})
		}
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeInherits && e.Meta["via"] != "mixin" {
			ix.superclassIDs[e.From] = append(ix.superclassIDs[e.From], e.To)
		}
	}
	for k := range ix.classesInFile {
		sortByLine(ix.classesInFile[k])
	}
	return ix
}

// modelResolution is the table a class was found to own, and how.
type modelResolution struct {
	table string
	via   string
	hops  int
}

// modelClass pairs a class node with what its own body declares about its
// storage, plus the file it was read from.
type modelClass struct {
	classAtLine
	decl railsModelDecl
	file string
}

// readFile collects the classes declared in one app/models file. absFile is
// the absolute path the tree cache is keyed by; class nodes carry the
// cwd-relative form, so both spellings are needed.
func (ix *modelTableIndex) readFile(svc, absFile string) []modelClass {
	relFile := patterns.RelativizeToCwd(absFile)
	classes := ix.classesInFile[jobFileKey(svc, relFile)]
	if len(classes) == 0 {
		return nil
	}
	src, root, release, ok := rubyParse(absFile)
	if !ok {
		return nil
	}
	defer release()

	decls := railsModelDeclarations(root, src)
	out := make([]modelClass, 0, len(classes))
	for _, c := range classes {
		out = append(out, modelClass{classAtLine: c, decl: decls[c.line], file: relFile})
	}
	return out
}

// linkService resolves one service's models in two rounds, because Rails'
// single-table inheritance makes the answer for one class depend on the
// answer for another: `class Issue < Comment` is stored in `comments`, and
// there is no `issues` table to find. Round one settles every class that can
// answer on its own (an explicit table_name, or a conventional name the
// schema confirms); round two hands each remaining model the table its
// nearest resolved ancestor owns.
func (ix *modelTableIndex) linkService(svc string, models []modelClass, owned map[string]bool) ([]graph.Edge, []graph.UnresolvedRef) {
	tables := ix.tablesByService[svc]

	resolved := map[string]modelResolution{} // class node ID -> its own table
	var pending []modelClass                 // reached a root, found no table of its own

	for _, m := range models {
		if m.decl.isModule || m.decl.abstract {
			// A module is never backed by a table, and `self.abstract_class =
			// true` says so outright. Neither is a failure to resolve, so
			// neither ledgers.
			continue
		}
		name, via, hops, resolvable := ix.resolveTable(m, tables)
		if !resolvable {
			continue // a PORO under app/models — not a model, not a miss
		}
		if _, found := tables[name]; !found {
			pending = append(pending, m)
			continue
		}
		resolved[m.id] = modelResolution{table: name, via: via, hops: hops}
	}

	var edges []graph.Edge
	var ledger []graph.UnresolvedRef
	emit := func(m modelClass, r modelResolution) {
		tid := tables[r.table]
		owned[tid] = true
		meta := map[string]string{"via": r.via, "table": r.table}
		if r.via == "convention" {
			meta["inherit_hops"] = fmt.Sprintf("%d", r.hops)
		}
		edges = append(edges, graph.Edge{
			ID:         fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeBackedBy), m.id, tid),
			From:       m.id,
			To:         tid,
			Type:       graph.EdgeTypeBackedBy,
			Confidence: graph.ConfidenceInferred,
			Meta:       meta,
		})
	}
	for _, m := range models {
		if r, ok := resolved[m.id]; ok {
			emit(m, r)
		}
	}
	for _, m := range pending {
		if r, ok := ix.stiAncestor(m.id, resolved); ok {
			emit(m, modelResolution{table: r, via: "sti"})
			continue
		}
		ledger = append(ledger, graph.UnresolvedRef{
			Service: svc, File: m.file, Line: m.line,
			Name: m.name, Kind: ledgerModelTableUnresolved,
		})
	}
	return edges, ledger
}

// stiAncestor walks real superclasses upward for the nearest one that owns a
// table. Superclass edges only — an `include`d concern is a module, and a
// module has no table to lend. Bounded by the same hop budget as the root
// walk: an STI chain deeper than a project's real inheritance depth is a sign
// the walk has wandered, not that the chain is long.
func (ix *modelTableIndex) stiAncestor(classID string, resolved map[string]modelResolution) (string, bool) {
	seen := map[string]bool{classID: true}
	frontier := []string{classID}
	for hop := 0; hop < maxModelInheritHops; hop++ {
		var next []string
		for _, id := range frontier {
			for _, super := range ix.superclassIDs[id] {
				if seen[super] {
					continue
				}
				seen[super] = true
				if r, ok := resolved[super]; ok {
					return r.table, true
				}
				next = append(next, super)
			}
		}
		if len(next) == 0 {
			return "", false
		}
		frontier = next
	}
	return "", false
}

// resolveTable decides which table name a class claims. resolvable is false
// for a class this pass has no business linking at all — a plain PORO under
// app/models that never reaches an ActiveRecord root — which is a silent
// skip, not a ledger row.
//
// An explicit `self.table_name` is authoritative and needs no inheritance
// proof: writing it is itself the declaration that this class is backed by a
// table. Only the conventional path has to establish that the class is an
// ActiveRecord model, because naive pluralization of an arbitrary class name
// would otherwise start claiming tables.
func (ix *modelTableIndex) resolveTable(m modelClass, tables map[string]string) (name, via string, hops int, resolvable bool) {
	if m.decl.tableName != "" {
		return m.decl.tableName, "table_name", 0, true
	}
	hops, reached, exhausted := ix.hopsToRoot(m.id)
	if exhausted {
		// Still climbing at the bound: this may well be a model, but proving
		// it would mean guessing past the bound. Fall through to a ledger row
		// by claiming a name no table set can contain.
		return "", "convention", 0, true
	}
	if !reached {
		return "", "", 0, false
	}
	cands := railsTableCandidates(m.name)
	var hits []string
	for _, cand := range cands {
		if _, ok := tables[cand]; ok {
			hits = append(hits, cand)
		}
	}
	if len(hits) != 1 {
		// Zero hits is the irregular plural (Person -> people) the plan
		// routes to the ledger on purpose. More than one is a genuine
		// ambiguity between two declared tables, and "fan out, never
		// first-match" would mean minting two owners for one class — so it
		// ledgers too, under the same kind.
		return "", "convention", hops, true
	}
	return hits[0], "convention", hops, true
}

// hopsToRoot walks `inherits` edges upward from classID. reached reports that
// an ActiveRecord root was found within maxModelInheritHops (hops is the
// distance to it); exhausted reports that the walk still had unexplored
// superclasses when it hit the bound, which is a "cannot tell", not a "no".
//
// Deliberately the same shape as jobInheritIndex.hopsToRoot (Tier CJ) rather
// than a second inheritance walker: the question — "does this class reach a
// known framework root over inherits" — is identical, only the root set
// differs.
func (ix *modelTableIndex) hopsToRoot(classID string) (hops int, reached, exhausted bool) {
	if activeRecordRootNames[ix.classNameOf[classID]] {
		return 0, true, false
	}
	seen := map[string]bool{classID: true}
	frontier := []string{classID}
	for hop := 1; hop <= maxModelInheritHops; hop++ {
		var next []string
		for _, id := range frontier {
			for _, super := range ix.superclassIDs[id] {
				if seen[super] {
					continue
				}
				seen[super] = true
				if activeRecordRootNames[ix.classNameOf[super]] {
					return hop, true, false
				}
				next = append(next, super)
			}
		}
		if len(next) == 0 {
			return 0, false, false
		}
		frontier = next
	}
	return 0, false, true
}

// railsModelDecl is what a class body declares about its own storage.
// isModule records that the declaration is `module X`, not `class X`: the
// Ruby parser types both as a class node, and a module can never be backed by
// a table — cedar has 131 of them under app/models/concerns alone.
type railsModelDecl struct {
	tableName string
	abstract  bool
	isModule  bool
}

// railsModelDeclarations reads `self.table_name = …` and `self.abstract_class
// = true` out of every class body in a file, keyed by the class's 1-based
// declaration line — the same key the graph's class nodes carry.
//
// Only *direct* body statements of the class count. A `self.table_name =` set
// inside a method or a conditional is a runtime decision this pass will not
// pretend to have read; those classes fall through to the convention and, if
// that misses, to the ledger.
func railsModelDeclarations(root *sitter.Node, src []byte) map[int]railsModelDecl {
	out := map[int]railsModelDecl{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "class":
			line := int(n.StartPoint().Row) + 1
			out[line] = railsClassBodyDecl(n.ChildByFieldName("body"), src)
		case "module":
			out[int(n.StartPoint().Row)+1] = railsModelDecl{isModule: true}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

func railsClassBodyDecl(body *sitter.Node, src []byte) railsModelDecl {
	var d railsModelDecl
	if body == nil {
		return d
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		stmt := body.NamedChild(i)
		if stmt.Type() != "assignment" {
			continue
		}
		left, right := stmt.ChildByFieldName("left"), stmt.ChildByFieldName("right")
		if left == nil || right == nil || left.Type() != "call" {
			continue
		}
		recv, method := left.ChildByFieldName("receiver"), left.ChildByFieldName("method")
		if recv == nil || method == nil || recv.Type() != "self" {
			continue
		}
		switch method.Content(src) {
		case "table_name":
			// String and symbol forms are the same declaration; a
			// simple_symbol's text is `:legacy_things`, so the leading colon
			// comes off alongside the quotes.
			v := strings.TrimPrefix(patterns.StripStringLiteral(right.Content(src)), ":")
			if right.Type() == "string" || right.Type() == "simple_symbol" {
				d.tableName = v
			}
		case "abstract_class":
			d.abstract = right.Type() == "true"
		}
	}
	return d
}

// railsTableCandidates returns the conventional table names for a class name,
// most specific first. Every candidate is checked against the declared table
// set before it is used, which is what makes a *regular* pluralizer
// sufficient here: the job is not "pluralize English correctly", it is "offer
// the handful of regular forms and let the schema decide". An irregular
// plural (Person -> people, Foot -> feet) produces no hit and ledgers, which
// is the correct outcome — better than an inflection table that is wrong in a
// different set of cases and gives no signal when it is.
func railsTableCandidates(className string) []string {
	base := railsUnderscore(className)
	if base == "" {
		return nil
	}
	switch {
	case strings.HasSuffix(base, "y") && len(base) > 1 && !isEnglishVowel(base[len(base)-2]):
		return []string{base[:len(base)-1] + "ies"}
	case strings.HasSuffix(base, "s"), strings.HasSuffix(base, "x"),
		strings.HasSuffix(base, "z"), strings.HasSuffix(base, "ch"),
		strings.HasSuffix(base, "sh"):
		return []string{base + "es"}
	case strings.HasSuffix(base, "fe"):
		return []string{base[:len(base)-2] + "ves", base + "s"}
	case strings.HasSuffix(base, "f"):
		return []string{base[:len(base)-1] + "ves", base + "s"}
	}
	return []string{base + "s"}
}

func isEnglishVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}

// railsUnderscore is ActiveSupport's underscore() for the shapes a class name
// can actually take: it demodulizes (Rails serves Admin::Report from
// `reports` unless the module declares a table_name_prefix, which is itself
// an explicit declaration this pass reads separately), then converts
// CamelCase to snake_case with the usual acronym handling (APIKey ->
// api_key).
func railsUnderscore(className string) string {
	if i := strings.LastIndex(className, "::"); i >= 0 {
		className = className[i+2:]
	}
	rs := []rune(className)
	var b strings.Builder
	for i, r := range rs {
		if !unicode.IsUpper(r) {
			b.WriteRune(r)
			continue
		}
		prevIsLowerOrDigit := i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]))
		endsAcronym := i > 0 && unicode.IsUpper(rs[i-1]) && i+1 < len(rs) && unicode.IsLower(rs[i+1])
		if prevIsLowerOrDigit || endsAcronym {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
