package factpipe

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_gorm_tables.go is the "gorm_tables" hub provider (see hub.go) — the
// Tier FX FX.8.23 migration of internal/linker/gorm_tables.go's retired
// LinkGormModelTables (Tier GT).
//
// The roster's original blocker note called this "a new primitive: scan
// sibling Go files for a literal-returning method, no existing analogue" —
// that framing predates HubProvider (FX.8.44, 2026-09-15). What the pass
// actually needs — reading a model's own file for its `func (X) TableName()
// string { return "…" }` body, walking an enclosing method/func's source
// span for a local variable's declared type, and scanning a `.Model()`/
// `.Table()` chain a few lines above a finisher call — is exactly what a
// hub is for: real Go over the graph-so-far plus the service's file list.
// No new primitive; ported near-verbatim, gt-prefixed.
//
// Unlike LinkTables, this never mints a synthetic table node — a resolved
// name with no CREATE TABLE declaration indexed in the workspace ledgers
// gorm_table_unresolved and stops (see the retired file's own doc comment
// for the "phantom exec_configs" cautionary tale). Edge type (queries vs
// persists) can't vary row-to-row within one `emit:` relation, so this hub
// emits two distinct predicates instead of a single one with a Type column
// — the branch happens here, in Go, same as every other FX.8 hub's
// Conf-column idiom, just carried one step further since there's no shared
// edge shape to route through a `when:` string-equality gate.
//
// Runs once over the WHOLE graph (all services), unlike the FX.8.8/8.10
// per-service convention — this pass has no `nodes[0].Service`-style
// single-service fallback to get wrong: every lookup already keys off each
// node's own n.Service (schemaByService, modelTable), exactly like the
// retired Go's st.allNodes call. internal/indexer/link_passes.go's
// "gorm_model_tables" pass already ran this way (scopeSameServiceOnly only
// filters edge writes, not the input), so the dedicated pipeline.Run call
// passes every node/file, not a per-service loop.
func init() { RegisterHub("gorm_tables", gormTablesHub) }

const (
	gtQueryEdgePred       = "gorm_query_edge"       // (From, To, Model, Table)
	gtPersistEdgePred     = "gorm_persist_edge"     // (From, To, Model, Table)
	gtModelUnresolvedPred = "gorm_model_unresolved" // (Svc, File, Line, Model)
	gtTableUnresolvedPred = "gorm_table_unresolved" // (Svc, File, Line, Table)
	gtTableCollisionPred  = "gorm_table_collision"  // (Svc, File, Line, Table)
)

func gormTablesHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint, _ graph.SchemaConfig) []Fact {
	// service -> table label -> node IDs (schema.sql preferred on a clash).
	schemaByService := make(map[string]map[string][]string)
	schemaAnyService := make(map[string][]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeTable {
			continue
		}
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

	fileCache := make(map[string][]byte)
	readFile := func(file string) []byte {
		if src, ok := fileCache[file]; ok {
			return src
		}
		src, _ := os.ReadFile(file)
		fileCache[file] = src
		return src
	}

	// (1) explicit TableName() methods.
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeMethod || n.Label != "TableName" {
			continue
		}
		recv := n.Meta["receiver"]
		if recv == "" {
			continue
		}
		if lit := gtReturnStringLiteral(readFile(n.File), n.Line, n.EndLine); lit != "" {
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
		conv := gtGormTableConvention(n.Label)
		if _, ok := schemaByService[n.Service][conv]; ok {
			putModel(n.Service, n.Label, conv)
		}
	}

	methodsByFile := make(map[string][]*graph.Node)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeMethod && n.Meta["receiver"] != "" {
			methodsByFile[n.File] = append(methodsByFile[n.File], n)
		}
	}
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
		return gtLocalVarType(gtSourceSpan(readFile(file), best.Line, best.EndLine), varName)
	}

	tableForModel := func(service, model string) string {
		if model == "" {
			return ""
		}
		if mt := modelTable[service]; mt != nil {
			if t := mt[model]; t != "" {
				return t
			}
		}
		if conv := gtGormTableConvention(model); len(schemaByService[service][conv]) > 0 {
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
		cand := gtStripReceiverSuffix(best.Meta["receiver"])
		return cand, tableForModel(service, cand)
	}

	var out []Fact
	origin := func(file string, line int, pred string) Origin {
		return Origin{Kind: OriginPrimitive, File: file, Line: line, Pattern: pred}
	}
	seenEdge := make(map[string]bool)
	seenLedger := make(map[string]bool)
	ledger := func(pred, svc, file string, line int, name string) {
		key := pred + "\x00" + svc + "\x00" + file + "\x00" + fmt.Sprint(line) + "\x00" + name
		if seenLedger[key] {
			return
		}
		seenLedger[key] = true
		out = append(out, Fact{
			Pred:   pred,
			Args:   []Atom{Str(svc), Str(file), Int(int64(line)), Str(name)},
			Origin: origin(file, line, pred),
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
			table = stripStringLiteralLocal(n.Meta["table_name"])
			if f := strings.Fields(table); len(f) > 0 {
				table = f[0]
			}
		default:
			ident := gtGormModelIdent(n.Meta["target"])
			if ident != "" && unicode.IsUpper([]rune(ident)[0]) {
				model = ident
				table = tableForModel(n.Service, model)
			}
			if table == "" && ident != "" && unicode.IsLower([]rune(ident)[0]) {
				if lm := localModelIdent(n.File, n.Line, ident); lm != "" {
					if t := tableForModel(n.Service, lm); t != "" {
						model, table = lm, t
					}
				}
			}
			if table == "" {
				from := n.Line - 6
				if from < 1 {
					from = 1
				}
				if kind, name := gtGormChainModel(gtSourceSpan(readFile(n.File), from, n.Line)); name != "" {
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
					ledger(gtModelUnresolvedPred, n.Service, n.File, n.Line, model)
				}
				continue
			}
		}

		targets := schemaByService[n.Service][table]
		if len(targets) == 0 {
			targets = schemaAnyService[table]
		}
		targets = gtPreferSchemaSQL(nodes, targets)
		if len(targets) == 0 {
			ledger(gtTableUnresolvedPred, n.Service, n.File, n.Line, table)
			continue
		}
		if len(targets) > 1 {
			ledger(gtTableCollisionPred, n.Service, n.File, n.Line, table)
		}

		pred := gtQueryEdgePred
		if n.Meta["op"] == "persist" {
			pred = gtPersistEdgePred
		}
		if n.Meta["pattern"] == "gorm_persist_table" {
			if stmt := gtSourceSpan(readFile(n.File), n.Line, n.Line+4); stmt != "" && !gtGormStmtWrites(stmt) {
				pred = gtQueryEdgePred
			}
		}
		for _, tid := range targets {
			eid := pred + ":" + n.ID + "->" + tid
			if seenEdge[eid] {
				continue
			}
			seenEdge[eid] = true
			out = append(out, Fact{
				Pred:   pred,
				Args:   []Atom{Node(n.ID), Node(tid), Str(model), Str(table)},
				Origin: origin(n.File, n.Line, pred),
			})
		}
	}

	return out
}

// stripStringLiteralLocal mirrors internal/patterns.StripStringLiteral —
// internal/factpipe cannot import internal/patterns (see hub_rails_route_
// actions.go's doc comment for the same constraint), and this pass only
// ever strips the double/backtick-quoted `.Table("literal")` capture.
func stripStringLiteralLocal(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '`' && s[len(s)-1] == '`') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// --- ported structural helpers (gt-prefixed, from gorm_tables.go) ---

func gtPreferSchemaSQL(nodes []graph.Node, ids []string) []string {
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

var gtGormReceiverSuffixes = []string{
	"Repositories", "Repository", "Repo", "Storage", "Store", "DAO", "Dao",
	"Service", "Svc", "Manager", "Mgr", "Gorm", "Model", "DB", "Db",
}

func gtStripReceiverSuffix(recv string) string {
	recv = strings.TrimPrefix(recv, "*")
	for _, s := range gtGormReceiverSuffixes {
		if len(recv) > len(s) && strings.HasSuffix(recv, s) {
			return recv[:len(recv)-len(s)]
		}
	}
	return ""
}

func gtSourceSpan(src []byte, startLine, endLine int) string {
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

var gtGormWriteVerbRe = regexp.MustCompile(`\.(Create|Save|Delete|Update|Updates|UpdateColumn|UpdateColumns|FirstOrCreate|Association)\b`)

func gtGormStmtWrites(stmt string) bool { return gtGormWriteVerbRe.MatchString(stmt) }

var gtGormReturnLiteralRe = regexp.MustCompile("return\\s+[\"`]([^\"`]+)[\"`]")

func gtReturnStringLiteral(src []byte, startLine, endLine int) string {
	if len(src) == 0 || startLine <= 0 {
		return ""
	}
	if endLine < startLine {
		endLine = startLine + 3
	}
	lines := strings.Split(string(src), "\n")
	if startLine > len(lines) {
		return ""
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	span := strings.Join(lines[startLine-1:endLine], "\n")
	if m := gtGormReturnLiteralRe.FindStringSubmatch(span); m != nil {
		return m[1]
	}
	return ""
}

func gtGormModelIdent(target string) string {
	s := strings.TrimSpace(target)
	s = strings.TrimLeft(s, "&*")
	if i := strings.IndexAny(s, "{( \t\r\n["); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	if s == "" || !gtIsGoIdent(s) {
		return ""
	}
	return s
}

func gtLocalVarType(span, name string) string {
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

var gtGormChainModelRe = regexp.MustCompile(`\.(Model|Table)\(\s*["` + "`" + `]?[\*&]?(?:\[\])?(?:\w+\.)?(\w+)`)

func gtGormChainModel(span string) (kind, name string) {
	if m := gtGormChainModelRe.FindStringSubmatch(span); m != nil {
		return m[1], m[2]
	}
	return "", ""
}

func gtIsGoIdent(s string) bool {
	for i, r := range s {
		if r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return s != ""
}

func gtGormTableConvention(name string) string {
	snake := gtToSnakeCase(name)
	if snake == "" {
		return ""
	}
	switch {
	case strings.HasSuffix(snake, "s"), strings.HasSuffix(snake, "x"),
		strings.HasSuffix(snake, "z"), strings.HasSuffix(snake, "ch"),
		strings.HasSuffix(snake, "sh"):
		return snake + "es"
	case strings.HasSuffix(snake, "y") && len(snake) > 1 && !gtIsVowel(rune(snake[len(snake)-2])):
		return snake[:len(snake)-1] + "ies"
	default:
		return snake + "s"
	}
}

func gtIsVowel(r rune) bool {
	switch unicode.ToLower(r) {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}

func gtToSnakeCase(s string) string {
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
