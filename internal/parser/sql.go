package parser

import (
	"encoding/json"
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// SQLParser parses .sql files (SQ0): CREATE TABLE/CREATE VIEW declarations,
// with CREATE INDEX/DROP TABLE/ALTER TABLE folded onto the table node they
// mutate rather than minted as their own nodes.
type SQLParser struct{}

func (p *SQLParser) Language() string     { return "sql" }
func (p *SQLParser) Extensions() []string { return []string{".sql"} }

// sqlColumnMutationPatternNames are patterns/sql/schema.yaml's ALTER TABLE
// ADD/DROP COLUMN pattern names. An ALTER is a mutation of an existing table
// entity, not a new one (SQ0's deliverable), so these never reach
// patterns.MatchToGraph as ordinary definitions — they are pulled out here
// and merged onto the already-built table node by same-file name lookup,
// mirroring internal/parser/shell.go's treatment of its own cross-file-only
// invocation patterns.
var sqlColumnMutationPatternNames = map[string]bool{
	"sql_alter_add_column":  true,
	"sql_alter_drop_column": true,
}

// sqlMetaOnlyPatternNames are CREATE INDEX / DROP TABLE — captured as meta
// on the existing table node rather than minted as their own node (SQ0's
// "one node type, a meta discriminator" rule, same as CREATE VIEW's
// meta["kind"]="view", which — unlike these two — still goes through
// patterns.MatchToGraph as its own table node since a view IS a distinct
// named relation, not a mutation of an existing one).
var sqlMetaOnlyPatternNames = map[string]bool{
	"sql_create_index": true,
	"sql_drop_table":   true,
}

func (p *SQLParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher, cache SourceCache) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := readSource(file, cache)
	if err != nil {
		return nil, nil, nil, err
	}
	results, matchErr := matcher.Match("sql", file, src)

	var defResults, mutations, metaOnly []patterns.MatchResult
	for _, r := range results {
		switch {
		case sqlColumnMutationPatternNames[r.PatternName]:
			mutations = append(mutations, r)
		case sqlMetaOnlyPatternNames[r.PatternName]:
			metaOnly = append(metaOnly, r)
		default:
			defResults = append(defResults, r)
		}
	}

	nodes, edges, unresolved := patterns.MatchToGraph(service, defResults)
	setLanguage(nodes, "sql")

	// Table name (post quote-strip, rule 11) -> index into nodes. Same-file
	// only: a name collision across files, or an ALTER whose table lives in
	// a schema file not yet indexed in this workspace, is SQ1/SQ2's
	// per-service resolution concern, not this single-file pass's — it
	// degrades to "skip, never guess" (rule 12) rather than fabricating a
	// cross-file link here.
	byName := make(map[string]int, len(nodes))
	for i := range nodes {
		if nodes[i].Type != graph.NodeTypeTable {
			continue
		}
		if _, ok := byName[nodes[i].Label]; !ok {
			byName[nodes[i].Label] = i
		}
	}

	// SQ0: build meta["columns"] JSON for each CREATE TABLE from the
	// retained column_definitions node (r.KeyNodes["columns"]) — a
	// MatchResult's Captures is a flat map[string]string, so a repeated
	// per-column capture can't survive as a list; the whole column list is
	// captured once and walked directly here instead, the same KeyNodes-
	// based approach shell.go uses for its own per-capture tree inspection.
	for _, r := range defResults {
		if r.PatternName != "sql_create_table" {
			continue
		}
		idx, ok := byName[patterns.StripStringLiteral(r.Captures["name"])]
		if !ok {
			continue
		}
		if cols := sqlColumnsJSON(r.KeyNodes["columns"], src); cols != "" {
			if nodes[idx].Meta == nil {
				nodes[idx].Meta = map[string]string{}
			}
			nodes[idx].Meta["columns"] = cols
		}
	}

	for _, r := range mutations {
		idx, ok := byName[patterns.StripStringLiteral(r.Captures["name"])]
		if !ok {
			continue
		}
		col := patterns.StripStringLiteral(r.Captures["column"])
		op := "add_column"
		if r.PatternName == "sql_alter_drop_column" {
			op = "drop_column"
		}
		mergeSQLColumnMutation(&nodes[idx], col, op)
		edges = append(edges, graph.Edge{
			ID:         fmt.Sprintf("calls:%s->alter:%s:%s:%d", nodes[idx].ID, op, col, r.Line),
			From:       nodes[idx].ID,
			To:         nodes[idx].ID,
			Type:       graph.EdgeTypeCalls,
			Confidence: graph.ConfidenceStatic,
			Meta:       map[string]string{"via": "alter", "op": op, "column": col},
		})
	}

	for _, r := range metaOnly {
		idx, ok := byName[patterns.StripStringLiteral(r.Captures["name"])]
		if !ok {
			continue
		}
		if nodes[idx].Meta == nil {
			nodes[idx].Meta = map[string]string{}
		}
		switch r.PatternName {
		case "sql_drop_table":
			nodes[idx].Meta["dropped"] = "true"
		case "sql_create_index":
			appendSQLIndex(&nodes[idx], patterns.StripStringLiteral(r.Captures["index_name"]))
		}
	}

	return nodes, edges, unresolved, matchErr
}

// sqlColumn is one entry of a table node's meta["columns"] JSON — the
// NodeTypeStruct meta["fields"] precedent ({name, type, tag}) applied to SQL
// columns instead of Go struct fields.
type sqlColumn struct {
	Name    string `json:"name"`
	Type    string `json:"type,omitempty"`
	Dropped bool   `json:"dropped,omitempty"`
}

// sqlColumnsJSON walks a retained column_definitions node's direct
// column_definition children and returns their {name, type} list as JSON.
// Standalone `constraints` children (a standalone FOREIGN KEY block) are not
// columns and are skipped here — SQ1 walks them separately for the
// `references` edge. Returns "" when columnsNode is nil or empty (no column
// meta is set at all, rather than an empty "[]" — CREATE TABLE always has at
// least one column in real SQL, so an empty result means the retained node
// itself is missing, not a genuinely columnless table).
func sqlColumnsJSON(columnsNode *sitter.Node, src []byte) string {
	if columnsNode == nil {
		return ""
	}
	var cols []sqlColumn
	for i := 0; i < int(columnsNode.NamedChildCount()); i++ {
		child := columnsNode.NamedChild(i)
		if child.Type() != "column_definition" || child.NamedChildCount() == 0 {
			continue
		}
		col := sqlColumn{Name: patterns.StripStringLiteral(child.NamedChild(0).Content(src))}
		if child.NamedChildCount() > 1 {
			col.Type = child.NamedChild(1).Content(src)
		}
		cols = append(cols, col)
	}
	if len(cols) == 0 {
		return ""
	}
	b, err := json.Marshal(cols)
	if err != nil {
		return ""
	}
	return string(b)
}

// mergeSQLColumnMutation applies an ALTER TABLE ADD/DROP COLUMN fact onto an
// existing table node's meta["columns"] JSON: ADD appends a new entry (type
// unknown — the alter's own column_definition type is not captured, since
// this phase's acceptance bar is the edge, not the column's data type);
// DROP marks the existing entry dropped=true rather than removing it (the
// column's prior existence in the table's history stays visible).
func mergeSQLColumnMutation(node *graph.Node, col, op string) {
	var cols []sqlColumn
	if raw := node.Meta["columns"]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &cols)
	}
	switch op {
	case "add_column":
		cols = append(cols, sqlColumn{Name: col})
	case "drop_column":
		for i := range cols {
			if cols[i].Name == col {
				cols[i].Dropped = true
			}
		}
	}
	b, err := json.Marshal(cols)
	if err != nil {
		return
	}
	if node.Meta == nil {
		node.Meta = map[string]string{}
	}
	node.Meta["columns"] = string(b)
}

// appendSQLIndex records a CREATE INDEX name onto its table's
// meta["indexes"] JSON array (deduped), never minting an index node.
func appendSQLIndex(node *graph.Node, idxName string) {
	if idxName == "" {
		return
	}
	var names []string
	if raw := node.Meta["indexes"]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &names)
	}
	for _, n := range names {
		if n == idxName {
			return
		}
	}
	names = append(names, idxName)
	b, err := json.Marshal(names)
	if err != nil {
		return
	}
	node.Meta["indexes"] = string(b)
}

func init() {
	Register(&SQLParser{})
}
