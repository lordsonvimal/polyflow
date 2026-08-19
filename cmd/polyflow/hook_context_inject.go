package main

// PostToolUse hook: auto-augment Grep/grep/cat/sed/head/tail/Read with graph
// context, read directly out of the local .polyflow/graph.db.
//
// This is a Go port of the original cmd/polyflow/hookscripts/polyflow-context-inject.py
// hook. It exists as a `polyflow` subcommand instead of a standalone Python
// script so that installing the polyflow binary is enough to use the hook —
// no python3 (or any other interpreter) needs to be present on the machine.
//
// Never blocks anything: it lets the tool call run exactly as the agent
// intended, then appends a compact `[polyflow]` block — callers/callees for
// a grepped symbol, or the functions/methods declared in a
// cat/sed/head/tail/Read'd file plus their callers. The payoff lands on the
// FIRST matching call, not contingent on the agent choosing to retry
// differently. Deduplicated per session per target so re-inspecting the same
// symbol/file doesn't repeat the same block and inflate tokens across many
// calls.
//
// Fails open everywhere: any parse error, missing field, locked/missing db,
// or unexpected panic exits 0 with no output — a broken hook must never be
// able to break a tool call's real output.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	_ "modernc.org/sqlite"
)

var hookContextInjectCmd = &cobra.Command{
	Use:    "hook-context-inject",
	Short:  "PostToolUse hook: append graph context to grep/cat/Read tool output (internal)",
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		defer func() { recover() }() // fail open: a broken hook must never disrupt real output
		runHookContextInject(os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(hookContextInjectCmd)
}

const hookSeenDir = "/tmp/polyflow-context-injected"
const hookMaxContextChars = 600
const hookCallsEdge = "calls"

var hookFileViewCmds = map[string]bool{"cat": true, "sed": true, "head": true, "tail": true}
var hookGrepCmds = map[string]bool{"grep": true}
var hookShellOperators = map[string]bool{"|": true, "||": true, ";": true, "&&": true, "&": true}
var hookIdentifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var hookAlternationRe = regexp.MustCompile(`\\\||\|`)
var hookFileExtRe = regexp.MustCompile(`\.\w{1,6}$`)

type hookPayload struct {
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	Cwd       string         `json:"cwd"`
	SessionID string         `json:"session_id"`
}

// tokenizeShell splits a shell command into simple-command token lists,
// breaking only on unquoted |/||/;/&&/& operators — a naive regex split on
// those characters corrupts any segment whose quoted argument itself
// contains one of them (e.g. `grep "heartbeat\|Heartbeat" file.go`, an
// extremely common alternation-pattern grep).
func tokenizeShell(command string) []string {
	var tokens []string
	var cur strings.Builder
	inSingle, inDouble, escaped := false, false, false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case escaped:
			cur.WriteRune(c)
			escaped = false
		case c == '\\' && !inSingle:
			escaped = true
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case !inSingle && !inDouble && (c == '|' || c == ';' || c == '&'):
			flush()
			op := string(c)
			if i+1 < len(runes) && runes[i+1] == c && (c == '|' || c == '&') {
				op += string(c)
				i++
			}
			tokens = append(tokens, op)
		case !inSingle && !inDouble && (c == ' ' || c == '\t' || c == '\n'):
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	if inSingle || inDouble {
		return nil // unbalanced quotes — don't guess
	}
	return tokens
}

func commandSegments(command string) [][]string {
	tokens := tokenizeShell(command)
	var segments [][]string
	var current []string
	for _, tok := range tokens {
		if hookShellOperators[tok] {
			if len(current) > 0 {
				segments = append(segments, current)
			}
			current = nil
		} else {
			current = append(current, tok)
		}
	}
	if len(current) > 0 {
		segments = append(segments, current)
	}
	return segments
}

// grepPatternSymbols extracts candidate identifiers from a grep pattern,
// shared by the native Grep tool (a "pattern" arg) and Bash-invoked
// grep/rg (a positional arg after flags). Real usage skews heavily toward
// alternation ("heartbeat\|Heartbeat\|RunnerHeartbeat") rather than a bare
// identifier; split on \| or | and keep whichever alternatives are
// themselves clean identifiers.
func grepPatternSymbols(pattern string) []string {
	if hookIdentifierRe.MatchString(pattern) {
		return []string{pattern}
	}
	var candidates []string
	for _, c := range hookAlternationRe.Split(pattern, -1) {
		if hookIdentifierRe.MatchString(c) {
			candidates = append(candidates, c)
		}
	}
	return candidates
}

// extractTarget mirrors the Python hook's extract_target: returns mode
// "file" (a single cat/sed/head/tail/Read target path) or "symbol" (a list
// of candidate identifiers, tried in order), or "" if this call isn't one we
// care about. toolName disambiguates the native Grep tool (a "pattern" arg,
// no shell command to parse) from Bash, whose tool_input shape it shares
// (both have no file_path).
func extractTarget(toolName string, toolInput map[string]any) (mode, file string, symbols []string) {
	if fp, ok := toolInput["file_path"].(string); ok && fp != "" {
		return "file", fp, nil
	}
	if toolName == "Grep" {
		pattern, _ := toolInput["pattern"].(string)
		if pattern == "" {
			return "", "", nil
		}
		if candidates := grepPatternSymbols(pattern); len(candidates) > 0 {
			return "symbol", "", candidates
		}
		return "", "", nil
	}
	command, _ := toolInput["command"].(string)
	if command == "" {
		return "", "", nil
	}
	for _, seg := range commandSegments(command) {
		if len(seg) == 0 {
			continue
		}
		cmdName := filepath.Base(seg[0])
		var rest []string
		for _, t := range seg[1:] {
			if !strings.HasPrefix(t, "-") {
				rest = append(rest, t)
			}
		}
		if hookGrepCmds[cmdName] && len(rest) > 0 {
			if candidates := grepPatternSymbols(rest[0]); len(candidates) > 0 {
				return "symbol", "", candidates
			}
			continue
		}
		if hookFileViewCmds[cmdName] {
			for _, tok := range rest {
				if strings.Contains(tok, "/") || hookFileExtRe.MatchString(tok) {
					return "file", tok, nil
				}
			}
		}
	}
	return "", "", nil
}

func findPolyflowDB(startDir string) string {
	d, err := filepath.Abs(startDir)
	if err != nil {
		return ""
	}
	for i := 0; i < 6; i++ {
		cand := filepath.Join(d, ".polyflow", "graph.db")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return ""
}

func hookRelPath(filePath, cwd string) string {
	if cwd != "" && strings.HasPrefix(filePath, cwd) {
		return strings.TrimPrefix(strings.TrimPrefix(filePath, cwd), "/")
	}
	return filePath
}

func relatedLabels(db *sql.DB, nodeID, direction string, limit int) []string {
	var query string
	if direction == "in" {
		query = `SELECT n.label FROM edges e JOIN nodes n ON e."from"=n.id WHERE e."to"=? AND e.type=? LIMIT ?`
	} else {
		query = `SELECT n.label FROM edges e JOIN nodes n ON e."to"=n.id WHERE e."from"=? AND e.type=? LIMIT ?`
	}
	rows, err := db.Query(query, nodeID, hookCallsEdge, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var l string
		if rows.Scan(&l) == nil && l != "" {
			labels = append(labels, l)
		}
	}
	return labels
}

type hookNodeRow struct {
	id, ntype, label, file string
	line                   int
}

func symbolContext(db *sql.DB, term string) []string {
	rows, err := db.Query(
		`SELECT id, type, label, file, line FROM nodes WHERE label = ? COLLATE NOCASE LIMIT 3`, term)
	if err != nil {
		return nil
	}
	nodeRows := scanHookNodeRows(rows)
	if len(nodeRows) == 0 {
		// A grepped term is very often a substring of the real symbol
		// (searching "heartbeat" to find sendHeartbeat/PublishHeartbeat).
		// Capped tighter (2, not 3) since a substring match is less precise.
		rows, err = db.Query(
			`SELECT id, type, label, file, line FROM nodes WHERE label LIKE ? COLLATE NOCASE LIMIT 2`, "%"+term+"%")
		if err != nil {
			return nil
		}
		nodeRows = scanHookNodeRows(rows)
	}
	if len(nodeRows) == 0 {
		return nil
	}
	var parts []string
	for _, n := range nodeRows {
		loc := ""
		if n.file != "" {
			loc = fmt.Sprintf("%s:%d", n.file, n.line)
		}
		seg := strings.TrimSpace(fmt.Sprintf("%s (%s) %s", n.label, n.ntype, loc))
		if callers := relatedLabels(db, n.id, "in", 4); len(callers) > 0 {
			seg += " | callers: " + strings.Join(callers, ", ")
		}
		if callees := relatedLabels(db, n.id, "out", 4); len(callees) > 0 {
			seg += " | calls: " + strings.Join(callees, ", ")
		}
		parts = append(parts, seg)
	}
	return parts
}

// fileContext returns every node declared in the file except the file node
// itself — not filtered to function/method/etc, since a file whose top-level
// declarations are struct/variable/http_client/etc still deserves context.
func fileContext(db *sql.DB, filePath, cwd string) []string {
	rel := hookRelPath(filePath, cwd)
	rows, err := db.Query(
		`SELECT id, type, label, line FROM nodes WHERE file = ? AND type != 'file' ORDER BY line LIMIT 8`, rel)
	if err != nil {
		return nil
	}
	// Buffer all rows before issuing any nested query: db has
	// SetMaxOpenConns(1), so running relatedLabels' own db.Query while these
	// rows are still open would deadlock waiting for the single connection
	// this same *sql.Rows is holding.
	type declRow struct {
		id, ntype, label string
		line             int
	}
	var decls []declRow
	for rows.Next() {
		var d declRow
		if rows.Scan(&d.id, &d.ntype, &d.label, &d.line) == nil {
			decls = append(decls, d)
		}
	}
	rows.Close()

	var parts []string
	for _, d := range decls {
		seg := fmt.Sprintf("%s(%s):%d", d.label, d.ntype, d.line)
		if callers := relatedLabels(db, d.id, "in", 3); len(callers) > 0 {
			seg += " <- " + strings.Join(callers, ", ")
		}
		parts = append(parts, seg)
	}
	return parts
}

func scanHookNodeRows(rows *sql.Rows) []hookNodeRow {
	defer rows.Close()
	var out []hookNodeRow
	for rows.Next() {
		var n hookNodeRow
		if rows.Scan(&n.id, &n.ntype, &n.label, &n.file, &n.line) == nil {
			out = append(out, n)
		}
	}
	return out
}

func hookAlreadySeen(sessionID, key string) bool {
	if sessionID == "" {
		return false
	}
	path := filepath.Join(hookSeenDir, sessionID+".json")
	seen := map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		var list []string
		if json.Unmarshal(data, &list) == nil {
			for _, k := range list {
				seen[k] = true
			}
		}
	}
	if seen[key] {
		return true
	}
	seen[key] = true
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if os.MkdirAll(hookSeenDir, 0o755) == nil {
		if data, err := json.Marshal(keys); err == nil {
			_ = os.WriteFile(path, data, 0o644)
		}
	}
	return false
}

func runHookContextInject(in *os.File, out *os.File) {
	var payload hookPayload
	if err := json.NewDecoder(in).Decode(&payload); err != nil {
		return
	}

	mode, file, symbols := extractTarget(payload.ToolName, payload.ToolInput)
	if mode == "" {
		return
	}

	cwd := payload.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	target := file
	if mode == "symbol" {
		target = strings.Join(symbols, "|")
	}
	dbPath := findPolyflowDB(cwd)
	if dbPath == "" {
		return
	}

	// Normalize file-mode keys to a repo-relative path so `cat foo/bar.go`
	// and a later Read of the absolute path dedupe against each other.
	var dedupeKey string
	if mode == "file" {
		dedupeKey = "file:" + hookRelPath(file, cwd)
	} else {
		dedupeKey = "symbol:" + target
	}
	if hookAlreadySeen(payload.SessionID, dedupeKey) {
		return
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=2000"); err != nil {
		return
	}
	if _, err := db.Exec("PRAGMA query_only=1"); err != nil {
		return
	}

	var parts []string
	var label string
	if mode == "symbol" {
		for _, candidate := range symbols {
			parts = symbolContext(db, candidate)
			if len(parts) > 0 {
				label = fmt.Sprintf("symbol '%s'", candidate)
				break
			}
		}
	} else {
		parts = fileContext(db, file, cwd)
		label = fmt.Sprintf("file '%s'", file)
	}
	if len(parts) == 0 {
		return
	}

	block := fmt.Sprintf("[polyflow graph — %s] %s", label, strings.Join(parts, "; "))
	if len(block) > hookMaxContextChars {
		block = block[:hookMaxContextChars] + "…"
	}

	data, err := json.Marshal(map[string]string{"additionalContext": block})
	if err != nil {
		return
	}
	fmt.Fprintln(out, string(data))
}
