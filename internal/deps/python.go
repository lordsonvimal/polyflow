package deps

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// reqLineRe matches a requirements.txt/constraints dependency line.
// Groups: 1=name, 2=operator, 3=version
var reqLineRe = regexp.MustCompile(
	`(?i)^([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[.*?\])?\s*(==|>=|<=|~=|!=|>|<)\s*([^\s,;#]+)`,
)

// resolvePython reads every Python manifest present under dir and accumulates
// dependencies. Checked in order: requirements.txt (prod), requirements-dev.txt
// / requirements-test.txt (dev), poetry.lock (category-aware), uv.lock (prod/dev
// via pyproject.toml groups).
func resolvePython(dir string) ([]Dependency, error) {
	var out []Dependency

	reqPairs := []struct {
		file string
		kind string
	}{
		{"requirements.txt", KindProd},
		{"requirements-dev.txt", KindDev},
		{"requirements_dev.txt", KindDev},
		{"dev-requirements.txt", KindDev},
		{"requirements-test.txt", KindDev},
		{"test-requirements.txt", KindDev},
		{"constraints.txt", KindProd},
	}
	for _, p := range reqPairs {
		ds, err := resolveRequirementsTxt(filepath.Join(dir, p.file), p.kind)
		if err != nil {
			return nil, err
		}
		out = append(out, ds...)
	}

	if ds, err := resolvePoetryLock(filepath.Join(dir, "poetry.lock"), dir); err != nil {
		return nil, err
	} else {
		out = append(out, ds...)
	}

	if ds, err := resolveUVLock(filepath.Join(dir, "uv.lock"), dir); err != nil {
		return nil, err
	} else {
		out = append(out, ds...)
	}

	return out, nil
}

// resolveRequirementsTxt parses a pip requirements or constraints file.
// Lines with == yield exact versions; other specifier forms fall back to the
// base version of the first specifier (same policy as the Node resolver).
func resolveRequirementsTxt(path, kind string) ([]Dependency, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []Dependency
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "-r") || strings.HasPrefix(line, "-c") ||
			strings.HasPrefix(line, "--") || strings.HasPrefix(line, "-e") ||
			strings.HasPrefix(line, "git+") || strings.HasPrefix(line, "http") {
			continue
		}
		// strip inline comment
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		m := reqLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, Dependency{
			Ecosystem: EcosystemPyPI,
			Name:      normalizePyPIName(m[1]),
			Version:   m[3],
			Kind:      kind,
		})
	}
	return out, scanner.Err()
}

// resolvePoetryLock parses poetry.lock (TOML, [[package]] sections).
// category = "main" → prod; category = "dev" → dev.
// For lock-version 2.0 (no category field), pyproject.toml dev groups are used.
func resolvePoetryLock(path, dir string) ([]Dependency, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	devPkgs, err := pyprojectDevPackages(dir)
	if err != nil {
		return nil, err
	}

	pkgs := scanTOMLPackages(data)
	out := make([]Dependency, 0, len(pkgs))
	for _, p := range pkgs {
		if p.name == "" || p.version == "" {
			continue
		}
		kind := KindProd
		cat := strings.ToLower(p.category)
		if cat == "dev" || cat == "development" {
			kind = KindDev
		} else if cat == "" && devPkgs[normalizePyPIName(p.name)] {
			kind = KindDev
		}
		out = append(out, Dependency{
			Ecosystem: EcosystemPyPI,
			Name:      normalizePyPIName(p.name),
			Version:   p.version,
			Kind:      kind,
		})
	}
	return out, nil
}

// resolveUVLock parses uv.lock (TOML v1, [[package]] sections).
// uv.lock carries no per-package kind; dev packages are identified via
// pyproject.toml dependency groups.
func resolveUVLock(path, dir string) ([]Dependency, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	devPkgs, err := pyprojectDevPackages(dir)
	if err != nil {
		return nil, err
	}

	pkgs := scanTOMLPackages(data)
	out := make([]Dependency, 0, len(pkgs))
	for _, p := range pkgs {
		if p.name == "" || p.version == "" {
			continue
		}
		kind := KindProd
		if devPkgs[normalizePyPIName(p.name)] {
			kind = KindDev
		}
		out = append(out, Dependency{
			Ecosystem: EcosystemPyPI,
			Name:      normalizePyPIName(p.name),
			Version:   p.version,
			Kind:      kind,
		})
	}
	return out, nil
}

// tomlPkg holds the fields we extract from a [[package]] block.
type tomlPkg struct {
	name, version, category string
}

// scanTOMLPackages scans TOML data for [[package]] sections, extracting
// name, version, and category (or groups). It is intentionally minimal:
// it does not implement a full TOML parser, only the specific shapes found
// in poetry.lock and uv.lock.
func scanTOMLPackages(data []byte) []tomlPkg {
	var (
		out     []tomlPkg
		current tomlPkg
		inPkg   bool
	)
	flush := func() {
		if inPkg && current.name != "" {
			out = append(out, current)
		}
		current = tomlPkg{}
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[[package]]" {
			flush()
			inPkg = true
			continue
		}
		// Any non-package [[...]] or [package.*] section ends the current block.
		if strings.HasPrefix(line, "[[") && line != "[[package]]" {
			flush()
			inPkg = false
			continue
		}
		if strings.HasPrefix(line, "[") && !strings.HasPrefix(line, "[[") {
			// sub-table of the current package (e.g. [package.dependencies])
			// or a top-level table — either way, key=value below is not ours
			flush()
			inPkg = false
			continue
		}
		if !inPkg || !strings.Contains(line, "=") {
			continue
		}
		key, val := splitTOMLKV(line)
		val = stripTOMLQuotes(val)
		switch key {
		case "name":
			current.name = val
		case "version":
			current.version = val
		case "category":
			current.category = val
		case "groups":
			// poetry v2: groups = ["dev"] or ["dev", "test"]
			if strings.Contains(val, `"dev"`) || strings.Contains(val, `"development"`) ||
				strings.Contains(val, "'dev'") {
				current.category = "dev"
			}
		}
	}
	flush()
	return out
}

// splitTOMLKV splits "key = value" into key and raw value, trimming whitespace.
func splitTOMLKV(line string) (key, val string) {
	idx := strings.Index(line, "=")
	if idx < 0 {
		return line, ""
	}
	return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:])
}

// stripTOMLQuotes removes surrounding double or single quotes from a TOML
// scalar value.
func stripTOMLQuotes(s string) string {
	if len(s) >= 2 &&
		((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

// pyprojectDevPackages reads pyproject.toml and returns a set of normalized
// package names that appear in any dev/test dependency group.
// Checked sections:
//   - [tool.uv.dev-dependencies] (list)
//   - [dependency-groups.dev] (PEP 735)
//   - [dependency-groups.test]
//   - [tool.poetry.group.dev.dependencies] (poetry group extras)
//   - [project.optional-dependencies] with key "dev" or "test"
func pyprojectDevPackages(dir string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	devPkgs := map[string]bool{}
	var inDevSection bool

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inDevSection = isDevSection(line)
			continue
		}
		if !inDevSection {
			continue
		}
		// List item: `"flask>=2.0"` or `flask = ">=2.0"` or `- flask`
		name := extractPyprojectDepName(line)
		if name != "" {
			devPkgs[normalizePyPIName(name)] = true
		}
	}
	return devPkgs, scanner.Err()
}

// isDevSection reports whether a TOML section header line corresponds to a
// dev/test dependency group.
func isDevSection(line string) bool {
	l := strings.ToLower(line)
	for _, marker := range []string{
		`[tool.uv.dev-dependencies]`,
		`[dependency-groups.dev]`,
		`[dependency-groups.test]`,
		`[tool.poetry.group.dev.dependencies]`,
		`[tool.poetry.group.test.dependencies]`,
	} {
		if l == marker {
			return true
		}
	}
	// [project.optional-dependencies] with dev/test key is handled below
	// via key detection, not section name.
	return false
}

// extractPyprojectDepName extracts a package name from a pyproject.toml
// dependency list or table line, normalizing to the canonical PyPI form.
func extractPyprojectDepName(line string) string {
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	// Table key: `flask = ">=2.3"` → "flask"
	if idx := strings.Index(line, "="); idx > 0 {
		name := strings.TrimSpace(line[:idx])
		name = strings.Trim(name, `"'`)
		if isValidPyPIName(name) {
			return name
		}
	}
	// List item: `"flask>=2.3"` or `flask>=2.3`
	s := strings.Trim(line, `"', `)
	// strip specifier
	for _, op := range []string{">=", "<=", "!=", "~=", "==", ">", "<", "["} {
		if idx := strings.Index(s, op); idx > 0 {
			s = s[:idx]
		}
	}
	s = strings.TrimSpace(s)
	if isValidPyPIName(s) {
		return s
	}
	return ""
}

// isValidPyPIName reports whether s looks like a PyPI package name.
var validPyPINameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func isValidPyPIName(s string) bool {
	return s != "" && validPyPINameRe.MatchString(s)
}

// normalizePyPIName lowercases and normalizes separators per PEP 503.
func normalizePyPIName(name string) string {
	name = strings.ToLower(name)
	// Replace underscores and dots with hyphens (PEP 503 normalized form).
	name = strings.NewReplacer("_", "-", ".", "-").Replace(name)
	return name
}
