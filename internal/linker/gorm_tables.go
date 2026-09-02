package linker

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// LinkGormModelTables (Tier GT) terminates GORM datastore call nodes at the
// schema-declared table they read or write. GORM call sites carry no literal
// SQL, so LinkTables (which keys off meta["sql"]) never reaches them — the
// table is implied by the Go model type in meta["target"], or named outright
// by a .Table("literal") call in meta["table_name"].
//
// Unlike LinkTables, this pass never mints a synthetic table node: if the
// resolved name has no CREATE TABLE declaration indexed in the workspace it
// ledgers gorm_table_unresolved and stops. Manufacturing a `table:` node
// from a naming convention is how the phantom `maple-manager:table:exec_configs`
// (next to the real `maple_exec_configs`) came to exist — this pass does not
// repeat that.
//
// Model → table name resolution, in order:
//  1. an explicit `func (X) TableName() string { return "…" }` on the model
//     (maple-manager declares one, maple_-prefixed, on nearly every model);
//  2. GORM's snake_case-plural convention — but only when the resulting name
//     matches a schema-declared table (never guessed into existence).
func LinkGormModelTables(nodes []graph.Node) (newEdges []graph.Edge, unresolved []graph.UnresolvedRef) {
	// service -> table label -> node IDs (schema.sql preferred on a clash).
	schemaByService := make(map[string]map[string][]string)
	schemaAnyService := make(map[string][]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeTable {
			continue
		}
		// Only a real CREATE TABLE declaration (a .sql file) is an
		// authoritative table. LinkTables also mints synthetic table nodes
		// from parsed SQL strings with an empty File — matching a GORM
		// convention name against one of those is how the phantom
		// `maple-manager:table:exec_configs` (beside the real
		// `maple_exec_configs`) would keep attracting edges.
		if !strings.HasSuffix(strings.ToLower(n.File), ".sql") {
			continue
		}
		if schemaByService[n.Service] == nil {
			schemaByService[n.Service] = make(map[string][]string)
		}
		schemaByService[n.Service][n.Label] = append(schemaByService[n.Service][n.Label], n.ID)
		schemaAnyService[n.Label] = append(schemaAnyService[n.Label], n.ID)
	}

	// service -> model struct name -> table name.
	modelTable := make(map[string]map[string]string)
	putModel := func(service, model, table string) {
		if service == "" || model == "" || table == "" {
			return
		}
		if modelTable[service] == nil {
			modelTable[service] = make(map[string]string)
		}
		if _, ok := modelTable[service][model]; !ok {
			modelTable[service][model] = table
		}
	}

	// (1) explicit TableName() methods. Read each declaring file once.
	fileCache := make(map[string][]byte)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeMethod || n.Label != "TableName" {
			continue
		}
		recv := n.Meta["receiver"]
		if recv == "" {
			continue
		}
		src, ok := fileCache[n.File]
		if !ok {
			src, _ = os.ReadFile(n.File)
			fileCache[n.File] = src
		}
		if lit := returnStringLiteral(src, n.Line, n.EndLine); lit != "" {
			putModel(n.Service, recv, lit)
		}
	}

	// (2) convention fallback, gated on a schema declaration existing.
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeStruct || n.Label == "" {
			continue
		}
		if modelTable[n.Service] != nil {
			if _, ok := modelTable[n.Service][n.Label]; ok {
				continue
			}
		}
		conv := gormTableConvention(n.Label)
		if _, ok := schemaByService[n.Service][conv]; ok {
			putModel(n.Service, n.Label, conv)
		}
	}

	seenEdge := make(map[string]bool)
	seenLedger := make(map[string]bool)
	ledger := func(svc, file string, line int, name, kind string) {
		key := kind + "\x00" + svc + "\x00" + file + "\x00" + fmt.Sprint(line) + "\x00" + name
		if seenLedger[key] {
			return
		}
		seenLedger[key] = true
		unresolved = append(unresolved, graph.UnresolvedRef{
			Service: svc, File: patterns.RelativizeToCwd(file), Line: line,
			Name: name, Kind: kind,
		})
	}

	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeDatastore || n.Meta["kind"] != "call" {
			continue
		}

		var table, model string
		switch {
		case n.Meta["table_name"] != "":
			table = patterns.StripStringLiteral(n.Meta["table_name"])
		default:
			model = gormModelIdent(n.Meta["target"])
			if model == "" {
				continue
			}
			// A lower-case identifier is a local variable, not an exported
			// model — its type needs SSA (v2). Skip without ledgering to
			// keep the ledger signal clean.
			if r := []rune(model)[0]; !unicode.IsUpper(r) {
				continue
			}
			if mt := modelTable[n.Service]; mt != nil {
				table = mt[model]
			}
			if table == "" {
				if conv := gormTableConvention(model); len(schemaByService[n.Service][conv]) > 0 {
					table = conv
				}
			}
			if table == "" {
				ledger(n.Service, n.File, n.Line, model, "gorm_model_unresolved")
				continue
			}
		}

		targets := schemaByService[n.Service][table]
		if len(targets) == 0 {
			targets = schemaAnyService[table]
		}
		targets = preferSchemaSQL(nodes, targets)
		if len(targets) == 0 {
			ledger(n.Service, n.File, n.Line, table, "gorm_table_unresolved")
			continue
		}
		if len(targets) > 1 {
			ledger(n.Service, n.File, n.Line, table, "gorm_table_collision")
		}

		edgeType := graph.EdgeTypeQueries
		if n.Meta["op"] == "persist" {
			edgeType = graph.EdgeTypePersists
		}
		for _, tid := range targets {
			eid := fmt.Sprintf("%s:%s->%s:gorm", string(edgeType), n.ID, tid)
			if seenEdge[eid] {
				continue
			}
			seenEdge[eid] = true
			m := map[string]string{"via": "gorm_model", "table": table}
			if model != "" {
				m["model"] = model
			}
			newEdges = append(newEdges, graph.Edge{
				ID: eid, From: n.ID, To: tid, Type: edgeType,
				Confidence: graph.ConfidenceStatic, Meta: m,
			})
		}
	}

	sort.Slice(newEdges, func(i, j int) bool { return newEdges[i].ID < newEdges[j].ID })
	return newEdges, unresolved
}

// preferSchemaSQL collapses a table-name clash between a db/schema.sql dump
// node and a db/migrations/*.sql node to just the schema.sql node(s). A
// genuine clash across two real schema files still fans out.
func preferSchemaSQL(nodes []graph.Node, ids []string) []string {
	if len(ids) < 2 {
		return ids
	}
	byID := make(map[string]string, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = nodes[i].File
	}
	var preferred []string
	for _, id := range ids {
		if filepath.Base(byID[id]) == "schema.sql" {
			preferred = append(preferred, id)
		}
	}
	if len(preferred) > 0 {
		return preferred
	}
	return ids
}

var gormReturnLiteralRe = regexp.MustCompile("return\\s+[\"`]([^\"`]+)[\"`]")

// returnStringLiteral pulls the first `return "…"` (or backtick) literal out
// of the source span [startLine, endLine] (both 1-based, inclusive).
func returnStringLiteral(src []byte, startLine, endLine int) string {
	if len(src) == 0 || startLine <= 0 {
		return ""
	}
	if endLine < startLine {
		endLine = startLine + 3 // TableName() bodies are one-liners
	}
	lines := strings.Split(string(src), "\n")
	if startLine > len(lines) {
		return ""
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	span := strings.Join(lines[startLine-1:endLine], "\n")
	if m := gormReturnLiteralRe.FindStringSubmatch(span); m != nil {
		return m[1]
	}
	return ""
}

// gormModelIdent extracts the model type name from a captured GORM target
// expression: "&models.ExecConfig{…}" -> "ExecConfig", "&user" -> "user",
// "&[]User{}" -> "". Returns "" when no bare identifier can be recovered.
func gormModelIdent(target string) string {
	s := strings.TrimSpace(target)
	s = strings.TrimLeft(s, "&*")
	// Cut at the first brace, paren, bracket, or whitespace.
	if i := strings.IndexAny(s, "{( \t\r\n["); i >= 0 {
		s = s[:i]
	}
	// Drop a package (or receiver) qualifier: keep the last dotted segment.
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	if s == "" || !isGoIdent(s) {
		return ""
	}
	return s
}

func isGoIdent(s string) bool {
	for i, r := range s {
		if r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return s != ""
}

// gormTableConvention applies GORM's default model→table naming: snake_case,
// then pluralized.
func gormTableConvention(name string) string {
	snake := toSnakeCase(name)
	if snake == "" {
		return ""
	}
	switch {
	case strings.HasSuffix(snake, "s"), strings.HasSuffix(snake, "x"),
		strings.HasSuffix(snake, "z"), strings.HasSuffix(snake, "ch"),
		strings.HasSuffix(snake, "sh"):
		return snake + "es"
	case strings.HasSuffix(snake, "y") && len(snake) > 1 && !isVowel(rune(snake[len(snake)-2])):
		return snake[:len(snake)-1] + "ies"
	default:
		return snake + "s"
	}
}

func isVowel(r rune) bool {
	switch unicode.ToLower(r) {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}

// toSnakeCase mirrors GORM's NamingStrategy: "ExecConfig" -> "exec_config",
// "APIKey" -> "api_key".
func toSnakeCase(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				next := rune(0)
				if i+1 < len(runes) {
					next = runes[i+1]
				}
				if !unicode.IsUpper(prev) || (unicode.IsUpper(prev) && next != 0 && unicode.IsLower(next)) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
