package linker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	sqlsitter "github.com/smacker/go-tree-sitter/sql"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// LinkSQLReferences resolves REFERENCES/FOREIGN KEY clauses (SQ1) into
// graph.EdgeTypeReferences edges between .sql-declared table nodes. Runs
// after every service's .sql files have been parsed (internal/parser/sql.go
// mints the table nodes during the main parse phase), re-parsing each .sql
// file independently — the same "linker doesn't reuse the parser's own
// Match results" shape internal/linker/shell_invocation.go already uses,
// needed here because resolving a REFERENCES target requires seeing every
// table in the service, not just the one file being parsed.
//
// Table-name resolution is scoped per service (the internal/linker/
// ruby_type_relations.go precedent, applied to table names instead of class
// names): a name collision across two schema files in the same service is
// genuinely ambiguous, so it fans out to every same-named candidate plus a
// ledger entry (sql_reference_collision) — never first-match. A REFERENCES
// target with zero same-service candidates (the schema file declaring it
// isn't indexed in this workspace, or it's a genuinely external table)
// ledgers as sql_reference_unresolved rather than being dropped silently.
func LinkSQLReferences(nodes []graph.Node, serviceFiles map[string][]string) (newEdges []graph.Edge, unresolved []graph.UnresolvedRef) {
	// service -> table name -> node IDs (fan-out capable; rule 1).
	byServiceName := make(map[string]map[string][]string)
	// service+"\x00"+relFile -> table name -> node ID (first match — a
	// same-file same-name redeclaration is malformed SQL, not something
	// this pass arbitrates).
	byFileName := make(map[string]map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeTable {
			continue
		}
		if byServiceName[n.Service] == nil {
			byServiceName[n.Service] = make(map[string][]string)
		}
		byServiceName[n.Service][n.Label] = append(byServiceName[n.Service][n.Label], n.ID)

		fk := n.Service + "\x00" + n.File
		if byFileName[fk] == nil {
			byFileName[fk] = make(map[string]string)
		}
		if _, ok := byFileName[fk][n.Label]; !ok {
			byFileName[fk][n.Label] = n.ID
		}
	}

	// Iterate services in a stable order (bug-class rule 2): map iteration
	// order over serviceFiles must never reach output.
	svcNames := make([]string, 0, len(serviceFiles))
	for svc := range serviceFiles {
		svcNames = append(svcNames, svc)
	}
	sort.Strings(svcNames)

	seenEdge := make(map[string]bool)
	seenUnresolved := make(map[string]bool)

	for _, svc := range svcNames {
		for _, file := range serviceFiles[svc] {
			if !isSQLFile(file) {
				continue
			}
			relFile := patterns.RelativizeToCwd(file)
			fromByName := byFileName[svc+"\x00"+relFile]
			if len(fromByName) == 0 {
				continue // internal/parser/sql.go mined no tables from this file
			}

			for _, decl := range parseSQLTableDecls(file) {
				fromID, ok := fromByName[decl.name]
				if !ok {
					continue
				}
				for _, fk := range decl.fks {
					candidates := byServiceName[svc][fk.refTable]
					switch len(candidates) {
					case 0:
						key := svc + "\x00" + relFile + "\x00" + fk.refTable + "\x00" + fmt.Sprint(fk.line)
						if !seenUnresolved[key] {
							seenUnresolved[key] = true
							unresolved = append(unresolved, graph.UnresolvedRef{
								Service: svc, File: relFile, Line: fk.line,
								Name: fk.refTable, Kind: "sql_reference_unresolved",
							})
						}
					case 1:
						if e, ok := sqlReferenceEdge(fromID, candidates[0], fk, seenEdge); ok {
							newEdges = append(newEdges, e)
						}
					default:
						key := svc + "\x00" + relFile + "\x00" + fk.refTable + "\x00" + fmt.Sprint(fk.line)
						if !seenUnresolved[key] {
							seenUnresolved[key] = true
							unresolved = append(unresolved, graph.UnresolvedRef{
								Service: svc, File: relFile, Line: fk.line,
								Name: fk.refTable, Kind: "sql_reference_collision",
							})
						}
						for _, to := range candidates {
							if e, ok := sqlReferenceEdge(fromID, to, fk, seenEdge); ok {
								newEdges = append(newEdges, e)
							}
						}
					}
				}
			}
		}
	}
	return
}

// sqlReferenceEdge builds a deduplicated `references` edge; ok=false when
// this exact (from, to, column) triple was already emitted.
func sqlReferenceEdge(fromID, toID string, fk sqlForeignKey, seen map[string]bool) (graph.Edge, bool) {
	eid := fmt.Sprintf("references:%s->%s:%s", fromID, toID, fk.column)
	if seen[eid] {
		return graph.Edge{}, false
	}
	seen[eid] = true
	return graph.Edge{
		ID:         eid,
		From:       fromID,
		To:         toID,
		Type:       graph.EdgeTypeReferences,
		Confidence: graph.ConfidenceStatic,
		Meta:       map[string]string{"column": fk.column, "ref_column": fk.refColumn},
	}, true
}

// isSQLFile reports whether file is a .sql file.
func isSQLFile(file string) bool {
	return strings.ToLower(filepath.Ext(file)) == ".sql"
}

// sqlForeignKey is one recognized REFERENCES clause, inline or standalone.
type sqlForeignKey struct {
	line      int
	column    string
	refTable  string
	refColumn string
}

// sqlTableDecl is one CREATE TABLE statement's name plus every FK clause
// found in its column list.
type sqlTableDecl struct {
	name string
	fks  []sqlForeignKey
}

// sqlCreateTableQuery mirrors patterns/sql/schema.yaml's sql_create_table
// query — kept as a literal Go string (not loaded from the YAML) because
// this pass re-parses independently of the patterns/matcher pipeline, the
// same decoupling internal/linker/shell_invocation.go's own query constants
// already use for their own cross-file queries.
const sqlCreateTableQuery = `(create_table
  (object_reference (identifier) @name)
  (column_definitions) @columns) @_stmt`

// parseSQLTableDecls walks file for every CREATE TABLE statement's name and
// FK clauses.
func parseSQLTableDecls(file string) []sqlTableDecl {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	lang := sqlsitter.GetLanguage()
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil || root == nil {
		return nil
	}
	q, err := compiledQuery(sqlCreateTableQuery, lang)
	if err != nil {
		return nil
	}

	var out []sqlTableDecl
	cur := sitter.NewQueryCursor()
	cur.Exec(q, root)
	for {
		m, ok := cur.NextMatch()
		if !ok {
			break
		}
		m = cur.FilterPredicates(m, src)
		if len(m.Captures) == 0 {
			continue
		}
		var nameNode, columnsNode *sitter.Node
		for _, c := range m.Captures {
			switch q.CaptureNameForId(c.Index) {
			case "name":
				nameNode = c.Node
			case "columns":
				columnsNode = c.Node
			}
		}
		if nameNode == nil {
			continue
		}
		out = append(out, sqlTableDecl{
			name: patterns.StripStringLiteral(nameNode.Content(src)),
			fks:  extractSQLForeignKeys(columnsNode, src),
		})
	}
	return out
}

// extractSQLForeignKeys walks a CREATE TABLE's column_definitions node for
// both FK shapes: an inline `col TYPE REFERENCES other(other_col)` on a
// column_definition, and a standalone `FOREIGN KEY (col) REFERENCES
// other(other_col)` inside a constraints block.
func extractSQLForeignKeys(columnsNode *sitter.Node, src []byte) []sqlForeignKey {
	if columnsNode == nil {
		return nil
	}
	var fks []sqlForeignKey
	for i := 0; i < int(columnsNode.NamedChildCount()); i++ {
		child := columnsNode.NamedChild(i)
		switch child.Type() {
		case "column_definition":
			if fk, ok := inlineForeignKey(child, src); ok {
				fks = append(fks, fk)
			}
		case "constraints":
			for j := 0; j < int(child.NamedChildCount()); j++ {
				cons := child.NamedChild(j)
				if cons.Type() != "constraint" {
					continue
				}
				if fk, ok := standaloneForeignKey(cons, src); ok {
					fks = append(fks, fk)
				}
			}
		}
	}
	return fks
}

// inlineForeignKey recognizes `col_name TYPE REFERENCES other(other_col)` on
// a single column_definition: identifier, type, object_reference, identifier
// as its named children, in that order (the REFERENCES clause is optional —
// most column_definitions have only the first two).
func inlineForeignKey(coldef *sitter.Node, src []byte) (sqlForeignKey, bool) {
	n := int(coldef.NamedChildCount())
	if n < 4 {
		return sqlForeignKey{}, false
	}
	colName := coldef.NamedChild(0)
	var refTableNode, refColNode *sitter.Node
	for i := 2; i < n; i++ {
		c := coldef.NamedChild(i)
		if c.Type() == "object_reference" {
			refTableNode = c
			if i+1 < n {
				refColNode = coldef.NamedChild(i + 1)
			}
			break
		}
	}
	if refTableNode == nil || refColNode == nil {
		return sqlForeignKey{}, false
	}
	return sqlForeignKey{
		line:      int(colName.StartPoint().Row) + 1,
		column:    patterns.StripStringLiteral(colName.Content(src)),
		refTable:  objectReferenceName(refTableNode, src),
		refColumn: patterns.StripStringLiteral(refColNode.Content(src)),
	}, true
}

// standaloneForeignKey recognizes `FOREIGN KEY (col) REFERENCES
// other(other_col)`: a constraint node whose named children are
// ordered_columns (wrapping the FK column), object_reference (the
// referenced table), and a trailing identifier (the referenced column).
func standaloneForeignKey(cons *sitter.Node, src []byte) (sqlForeignKey, bool) {
	var colsNode, refTableNode, refColNode *sitter.Node
	for i := 0; i < int(cons.NamedChildCount()); i++ {
		c := cons.NamedChild(i)
		switch c.Type() {
		case "ordered_columns":
			colsNode = c
		case "object_reference":
			refTableNode = c
		case "identifier":
			refColNode = c
		}
	}
	if colsNode == nil || refTableNode == nil || refColNode == nil {
		return sqlForeignKey{}, false
	}
	var colName string
	for i := 0; i < int(colsNode.NamedChildCount()); i++ {
		c := colsNode.NamedChild(i)
		if c.Type() == "column" && c.NamedChildCount() > 0 {
			colName = patterns.StripStringLiteral(c.NamedChild(0).Content(src))
			break
		}
	}
	if colName == "" {
		return sqlForeignKey{}, false
	}
	return sqlForeignKey{
		line:      int(cons.StartPoint().Row) + 1,
		column:    colName,
		refTable:  objectReferenceName(refTableNode, src),
		refColumn: patterns.StripStringLiteral(refColNode.Content(src)),
	}, true
}

// objectReferenceName returns an object_reference node's bare identifier
// text (quote-stripped).
func objectReferenceName(n *sitter.Node, src []byte) string {
	if n.NamedChildCount() > 0 {
		return patterns.StripStringLiteral(n.NamedChild(0).Content(src))
	}
	return patterns.StripStringLiteral(n.Content(src))
}
