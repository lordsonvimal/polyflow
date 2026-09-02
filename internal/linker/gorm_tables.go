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

	// Enclosing-method index: file -> methods with a receiver, so an
	// unresolvable local target (r.db.Create(config)) can fall back to the
	// <Model>Repository / <Model>Service receiver that owns the call.
	methodsByFile := make(map[string][]*graph.Node)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeMethod && n.Meta["receiver"] != "" {
			methodsByFile[n.File] = append(methodsByFile[n.File], n)
		}
	}
	// Callable scopes (methods + plain funcs) for recovering a local var's
	// declared type: `var setting models.Setting` … db.Create(&setting).
	callableByFile := make(map[string][]*graph.Node)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeMethod || n.Type == graph.NodeTypeFunction {
			callableByFile[n.File] = append(callableByFile[n.File], n)
		}
	}
	localModelIdent := func(file string, line int, varName string) string {
		if varName == "" {
			return ""
		}
		var best *graph.Node
		for _, m := range callableByFile[file] {
			end := m.EndLine
			if end < m.Line {
				end = m.Line
			}
			if line < m.Line || line > end {
				continue
			}
			if best == nil || m.Line > best.Line {
				best = m
			}
		}
		if best == nil {
			return ""
		}
		src, ok := fileCache[file]
		if !ok {
			src, _ = os.ReadFile(file)
			fileCache[file] = src
		}
		return localVarType(sourceSpan(src, best.Line, best.EndLine), varName)
	}

	// tableForModel resolves a model struct name to a schema table, applying
	// the TableName() map then the schema-gated convention. "" if neither.
	tableForModel := func(service, model string) string {
		if model == "" {
			return ""
		}
		if mt := modelTable[service]; mt != nil {
			if t := mt[model]; t != "" {
				return t
			}
		}
		if conv := gormTableConvention(model); len(schemaByService[service][conv]) > 0 {
			return conv
		}
		return ""
	}
	enclosingModelTable := func(service, file string, line int) (model, table string) {
		var best *graph.Node
		for _, m := range methodsByFile[file] {
			end := m.EndLine
			if end < m.Line {
				end = m.Line
			}
			if line < m.Line || line > end {
				continue
			}
			if best == nil || m.Line > best.Line {
				best = m
			}
		}
		if best == nil {
			return "", ""
		}
		cand := stripReceiverSuffix(best.Meta["receiver"])
		return cand, tableForModel(service, cand)
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
			// Drop a trailing SQL alias: .Table("maple_config_packages AS cp").
			if f := strings.Fields(table); len(f) > 0 {
				table = f[0]
			}
		default:
			ident := gormModelIdent(n.Meta["target"])
			// An exported identifier is (probably) a model type — resolve it
			// directly. A lower-case local, a column map, or an empty capture
			// falls through to the enclosing-receiver heuristic.
			if ident != "" && unicode.IsUpper([]rune(ident)[0]) {
				model = ident
				table = tableForModel(n.Service, model)
			}
			// A lower-case local (`&setting`) — recover its declared type from
			// the enclosing function body (`var setting models.Setting`).
			if table == "" && ident != "" && unicode.IsLower([]rune(ident)[0]) {
				if lm := localModelIdent(n.File, n.Line, ident); lm != "" {
					if t := tableForModel(n.Service, lm); t != "" {
						model, table = lm, t
					}
				}
			}
			// A `db.Model(&models.ExecConfig{}).Where(…).UpdateColumn(…)` chain:
			// the finisher node sits a couple lines below the .Model()/.Table()
			// that names the target. Scan back over the chain statement.
			if table == "" {
				src, ok := fileCache[n.File]
				if !ok {
					src, _ = os.ReadFile(n.File)
					fileCache[n.File] = src
				}
				from := n.Line - 6
				if from < 1 {
					from = 1
				}
				if kind, name := gormChainModel(sourceSpan(src, from, n.Line)); name != "" {
					if strings.EqualFold(kind, "Table") {
						if len(schemaByService[n.Service][name]) > 0 || len(schemaAnyService[name]) > 0 {
							table = name
						}
					} else if t := tableForModel(n.Service, name); t != "" {
						model, table = name, t
					}
				}
			}
			if table == "" {
				if m, t := enclosingModelTable(n.Service, n.File, n.Line); t != "" {
					model, table = m, t
				}
			}
			if table == "" {
				if model != "" {
					ledger(n.Service, n.File, n.Line, model, "gorm_model_unresolved")
				}
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
		// A .Table("x") call is classified persist by pattern name but is
		// just as often a scoped read (.Table("x").Where(...).Find(&y)).
		// Re-read the statement and downgrade to queries when only read
		// finishers appear.
		if n.Meta["pattern"] == "gorm_persist_table" {
			src, ok := fileCache[n.File]
			if !ok {
				src, _ = os.ReadFile(n.File)
				fileCache[n.File] = src
			}
			if stmt := sourceSpan(src, n.Line, n.Line+4); stmt != "" && !gormStmtWrites(stmt) {
				edgeType = graph.EdgeTypeQueries
			}
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

// gormReceiverSuffixes are the noun endings a GORM data-access type name
// carries around its model: ExecConfigRepository -> ExecConfig,
// UserService -> User. Ordered longest-first so "Repositories" wins over
// "Repository" etc.
var gormReceiverSuffixes = []string{
	"Repositories", "Repository", "Repo", "Storage", "Store", "DAO", "Dao",
	"Service", "Svc", "Manager", "Mgr", "Gorm", "Model", "DB", "Db",
}

// stripReceiverSuffix removes one trailing data-access suffix from a
// receiver type name. Returns "" if nothing is stripped (a bare "Foo"
// receiver is not evidence that the model is "Foo").
func stripReceiverSuffix(recv string) string {
	recv = strings.TrimPrefix(recv, "*")
	for _, s := range gormReceiverSuffixes {
		if len(recv) > len(s) && strings.HasSuffix(recv, s) {
			return recv[:len(recv)-len(s)]
		}
	}
	return ""
}

// sourceSpan returns lines [startLine, endLine] (1-based, inclusive) joined.
func sourceSpan(src []byte, startLine, endLine int) string {
	if len(src) == 0 || startLine <= 0 {
		return ""
	}
	lines := strings.Split(string(src), "\n")
	if startLine > len(lines) {
		return ""
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if endLine < startLine {
		endLine = startLine
	}
	return strings.Join(lines[startLine-1:endLine], "\n")
}

var gormWriteVerbRe = regexp.MustCompile(`\.(Create|Save|Delete|Update|Updates|UpdateColumn|UpdateColumns|FirstOrCreate|Association)\b`)

// gormStmtWrites reports whether a GORM chain statement contains a mutation
// finisher.
func gormStmtWrites(stmt string) bool { return gormWriteVerbRe.MatchString(stmt) }

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

// localVarType finds `var <name> <Type>` or `<name> :=/= <Type>{…}` within a
// function-body span and returns the unqualified type identifier ("" if none).
func localVarType(span, name string) string {
	if span == "" || name == "" {
		return ""
	}
	q := regexp.QuoteMeta(name)
	re := regexp.MustCompile(`(?m)(?:\bvar\s+` + q + `\s+|\b` + q + `\s*:?=\s*)[\*&]?(?:\[\])?(?:\w+\.)?([A-Z]\w*)`)
	if m := re.FindStringSubmatch(span); m != nil {
		return m[1]
	}
	return ""
}

var gormChainModelRe = regexp.MustCompile(`\.(Model|Table)\(\s*["` + "`" + `]?[\*&]?(?:\[\])?(?:\w+\.)?(\w+)`)

// gormChainModel pulls the model type or literal table out of a `.Model(&X{})`
// / `.Table("x")` call within a chain statement span. Returns ("Model", "X")
// or ("Table", "x"); ("", "") when neither appears.
func gormChainModel(span string) (kind, name string) {
	if m := gormChainModelRe.FindStringSubmatch(span); m != nil {
		return m[1], m[2]
	}
	return "", ""
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
