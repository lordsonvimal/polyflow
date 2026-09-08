package parser

import (
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// Tier AT.1 (docs/cedar-end-to-end-flow-holes-plan.md): the Rails half of
// SQ0's CREATE TABLE parsing. patterns/ruby/schema.yaml matches the
// `create_table "x" do |t| … end` call; everything path-dependent lives here,
// because a tree-sitter query cannot see the file it is running against.

// railsSchemaTablePattern is the one pattern this gate governs.
const railsSchemaTablePattern = "rails_create_table"

// railsSchemaColumnSourceMeta marks a table node as declared by a Rails
// schema dump rather than by a .sql CREATE TABLE. Consumed by
// linker.LinkRailsModelTables, which resolves models only against tables it
// can attribute to a real schema declaration.
const railsSchemaColumnSourceMeta = "rails_schema"

// railsSchemaBlockNonColumnMethods are the `create_table` block macros that
// declare something other than a column. A deny-list rather than an
// allow-list of column types: `t.string`/`t.bigint`/`t.jsonb`/… is an open
// set that grows with every Rails release and every custom type a project
// registers, while the non-column macros are a short, stable list. Anything
// else inside a create_table block is a column declaration.
var railsSchemaBlockNonColumnMethods = map[string]bool{
	"index":                true,
	"foreign_key":          true,
	"check_constraint":     true,
	"exclusion_constraint": true,
	"unique_constraint":    true,
}

// isRailsSchemaFile reports whether file is a Rails schema dump: db/schema.rb,
// or db/<name>_schema.rb for a multi-database app (`primary_schema.rb`,
// `analytics_schema.rb`). Deliberately NOT db/migrate/*.rb — migrations are a
// historical log in which the same table is created and dropped across dozens
// of files, so matching them mints duplicate and stale table nodes.
//
// file is expected cwd-relative (RubyParser relativizes before calling), but
// the check is on the trailing `db/` directory + filename, so an absolute
// path works too.
func isRailsSchemaFile(file string) bool {
	file = filepath.ToSlash(file)
	dir, base := filepath.Split(file)
	if !strings.HasSuffix(strings.TrimSuffix(dir, "/"), "db") {
		return false
	}
	return base == "schema.rb" || strings.HasSuffix(base, "_schema.rb")
}

// dropNonSchemaFileTableMatches removes rails_create_table matches outside a
// schema dump. Same shape (and same reason) as
// dropNonRoutesFileRouteMatches: the pattern is only meaningful in one
// conventional location, and admitting it elsewhere invents entities.
func dropNonSchemaFileTableMatches(file string, results []patterns.MatchResult) []patterns.MatchResult {
	if isRailsSchemaFile(file) {
		return results
	}
	out := results[:0]
	for _, r := range results {
		if r.PatternName == railsSchemaTablePattern {
			continue
		}
		out = append(out, r)
	}
	return out
}

// stampRailsSchemaTables replaces each Rails table node's raw meta["columns"]
// — which at this point holds the entire `do |t| … end` block source, since
// MatchResult.Captures is flat text — with the comma-joined column names, and
// records that the node came from a schema dump.
//
// Comma-joined, not the {name,type} JSON internal/parser/sql.go builds: AT's
// deliverable is which table a model owns, and a column *list* is what makes
// that node readable in search output. Nothing consumes column types yet, and
// a shape nothing reads is a shape nobody keeps correct.
func stampRailsSchemaTables(src []byte, nodes []graph.Node, results []patterns.MatchResult) {
	byLine := make(map[int]int, len(nodes)) // declaration line -> index into nodes
	for i := range nodes {
		if nodes[i].Type != graph.NodeTypeTable {
			continue
		}
		if _, seen := byLine[nodes[i].Line]; !seen {
			byLine[nodes[i].Line] = i
		}
	}
	for _, r := range results {
		if r.PatternName != railsSchemaTablePattern {
			continue
		}
		idx, ok := byLine[r.Line]
		if !ok {
			continue
		}
		n := &nodes[idx]
		if n.Meta == nil {
			n.Meta = map[string]string{}
		}
		n.Meta["source"] = railsSchemaColumnSourceMeta
		if cols := railsSchemaColumns(r.KeyNodes["columns"], src); cols != "" {
			n.Meta["columns"] = cols
		} else {
			// The block held no column declaration at all (a join table
			// written as `create_table "x", id: false do |t| end`). Drop the
			// raw block source rather than leaving it as a fake column list.
			delete(n.Meta, "columns")
		}
	}
}

// railsSchemaColumns walks a retained do_block and returns its column names,
// comma-joined in declaration order. A single call may declare several
// columns (`t.string "first", "last"`), so every leading string argument
// counts, not just the first.
func railsSchemaColumns(block *sitter.Node, src []byte) string {
	if block == nil {
		return ""
	}
	var cols []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "call" {
			recv := n.ChildByFieldName("receiver")
			method := n.ChildByFieldName("method")
			if recv != nil && method != nil && recv.Type() == "identifier" &&
				!railsSchemaBlockNonColumnMethods[method.Content(src)] {
				args := n.ChildByFieldName("arguments")
				if args != nil {
					for i := 0; i < int(args.NamedChildCount()); i++ {
						arg := args.NamedChild(i)
						if arg.Type() != "string" {
							// Keyword options (`null: false`) end the column
							// names; anything non-literal is not a name.
							break
						}
						if name := patterns.StripStringLiteral(arg.Content(src)); name != "" {
							cols = append(cols, name)
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(block)
	return strings.Join(cols, ",")
}
