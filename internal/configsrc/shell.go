package configsrc

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// shellEnvValues discovers *.sh/*.bash/*.bats files under dir (recursively)
// and extracts environment-variable values from two forms: `export
// NAME=value` and the leading-assignment idiom `NAME=value cmd` (a one-off
// env override for a single invocation — a bare `NAME=value` with no
// trailing command is a local shell variable, not necessarily an exported
// environment fact, and is deliberately NOT captured here). Each file is one
// environment overlay (fan-out for the same variable set differently across
// files — bug-class rule 1). Values built from command substitution
// (`` `date +%s` ``, `$(...)`) or another variable (`export X=$Y`) are not
// literal and are skipped entirely — the same literal-only stance
// k8sEnvValues/terraformEnvValues already take for their own dynamic/complex
// right-hand sides (terraform.go's readTFVars skips any value containing
// HCL interpolation syntax the same way). Source ref is "rel-path:line".
// Returns nil for missing/unreadable dirs (absence of config is a result,
// not an error).
func shellEnvValues(dir string) map[string][]Value {
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".sh" || ext == ".bash" || ext == ".bats" {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)

	result := make(map[string][]Value)
	for _, p := range files {
		rel, _ := filepath.Rel(dir, p)
		_ = readShellEnvFile(p, rel, result)
	}
	return result
}

// shellExportRe matches `export NAME=value` (optionally `export -p NAME=v`'s
// plain form is not handled — only the assignment form). Leading whitespace
// allowed (indented inside a function/if-block).
var shellExportRe = regexp.MustCompile(`^\s*export\s+([A-Za-z_][A-Za-z0-9_]*)=("[^"]*"|'[^']*'|\S*)\s*$`)

// shellLeadingAssignRe matches the leading-assignment idiom
// `NAME=value cmd ...` — the trailing `\s+\S` requires a command token after
// the assignment, distinguishing it from a bare local-variable assignment
// (`NAME=value` alone), which is not captured (it may never be exported).
var shellLeadingAssignRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)=("[^"]*"|'[^']*'|\S+)\s+\S.*$`)

func readShellEnvFile(path, relName string, out map[string][]Value) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		var name, raw string
		if m := shellExportRe.FindStringSubmatch(line); m != nil {
			name, raw = m[1], m[2]
		} else if m := shellLeadingAssignRe.FindStringSubmatch(line); m != nil {
			name, raw = m[1], m[2]
		} else {
			continue
		}

		// Command substitution / variable interpolation: not a literal
		// value — skip rather than guess (matches k8s/terraform's own
		// literal-only stance for unresolvable right-hand sides).
		if strings.ContainsAny(raw, "$`") {
			continue
		}
		val := stripValue(raw)
		if name == "" || val == "" {
			continue
		}
		out[name] = appendUnique(out[name], Value{Value: val, Ref: ref(relName, lineNum)})
	}
	return scanner.Err()
}
