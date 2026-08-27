package deps

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// resolveNode resolves npm dependencies. Declared names + prod/dev kind come
// from package.json; exact resolved versions come from the lockfile
// (package-lock.json v2/v3 or yarn.lock v1/berry). If no lockfile exists, the
// declared range is stripped to its base version as a best effort.
//
// A service whose own dir has no package.json walks up toward root looking
// for one, the same direction Node's own module resolution walks toward the
// filesystem root looking for node_modules — a workspace that splits one
// language into a subdirectory "service" (e.g. a Rails repo's `js` service
// pointed at ./app/javascript, package.json only at the repo root) otherwise
// resolves zero npm deps for that service, silently deactivating every
// package-version-gated pattern (react.yaml's lifecycle-hook reflect-dispatch
// among them — confirmed live on orion: the `js` service's own dir has no
// manifest, so react was never "seen" as a dependency and every lifecycle
// hook in app/javascript/**/*.jsx read as regular, zero-caller dead code).
// root bounds the walk; pass "" to disable it (dir must hold its own
// manifest, the pre-existing behavior every other caller still gets).
func resolveNode(dir, root string) ([]Dependency, error) {
	for {
		pkgPath := filepath.Join(dir, "package.json")
		data, err := os.ReadFile(pkgPath)
		if err == nil {
			return parseNodePackage(dir, data)
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		if root == "" || dir == root {
			return nil, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
}

// parseNodePackage reads dependencies/devDependencies from an already-loaded
// package.json and resolves their exact versions against dir's lockfile.
func parseNodePackage(dir string, data []byte) ([]Dependency, error) {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, err
	}

	resolved := map[string]string{}
	if r, err := readPackageLock(filepath.Join(dir, "package-lock.json")); err != nil {
		return nil, err
	} else if r != nil {
		resolved = r
	} else if r, err := readYarnLock(filepath.Join(dir, "yarn.lock")); err != nil {
		return nil, err
	} else if r != nil {
		resolved = r
	}

	var out []Dependency
	add := func(m map[string]string, kind string) {
		for name, rng := range m {
			version := resolved[name]
			if version == "" {
				version = stripRange(rng)
			}
			out = append(out, Dependency{
				Ecosystem: EcosystemNPM,
				Name:      name,
				Version:   version,
				Kind:      kind,
			})
		}
	}
	add(pkg.Dependencies, KindProd)
	add(pkg.DevDependencies, KindDev)
	return out, nil
}

// readPackageLock extracts name → exact version from package-lock.json.
// Supports lockfile v2/v3 ("packages" keyed by node_modules path) and
// v1 ("dependencies" keyed by name).
func readPackageLock(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for pkgPath, info := range lock.Packages {
		// "node_modules/foo" or "node_modules/@scope/foo"; nested
		// "node_modules/a/node_modules/b" entries resolve to the deepest name.
		idx := strings.LastIndex(pkgPath, "node_modules/")
		if idx < 0 {
			continue // root entry ""
		}
		name := pkgPath[idx+len("node_modules/"):]
		if _, exists := out[name]; !exists && info.Version != "" {
			out[name] = info.Version
		}
	}
	for name, info := range lock.Dependencies {
		if _, exists := out[name]; !exists && info.Version != "" {
			out[name] = info.Version
		}
	}
	return out, nil
}

var (
	// yarn v1 header: `"@scope/pkg@^1.0.0", "@scope/pkg@^1.2.0":` or `pkg@^1.0.0:`
	yarnHeaderRe  = regexp.MustCompile(`^"?(@?[^@"\s]+(?:/[^@"\s]+)?)@`)
	yarnVersionRe = regexp.MustCompile(`^\s{2}version:?\s+"?([^"\s]+)"?`)
)

// readYarnLock extracts name → exact version from yarn.lock (classic v1 and
// berry). Both formats use `name@range:` block headers followed by an
// indented `version` line.
func readYarnLock(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	var currentNames []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		// Block header: not indented, ends with ":"
		if len(line) > 0 && line[0] != ' ' && strings.HasSuffix(strings.TrimSpace(line), ":") {
			currentNames = currentNames[:0]
			for _, part := range strings.Split(strings.TrimSuffix(strings.TrimSpace(line), ":"), ",") {
				part = strings.TrimSpace(part)
				if m := yarnHeaderRe.FindStringSubmatch(part); m != nil {
					currentNames = append(currentNames, m[1])
				}
			}
			continue
		}
		if m := yarnVersionRe.FindStringSubmatch(line); m != nil {
			for _, name := range currentNames {
				if _, exists := out[name]; !exists {
					out[name] = m[1]
				}
			}
			currentNames = currentNames[:0]
		}
	}
	return out, nil
}

// stripRange reduces a declared semver range to its base version:
// "^1.2.3" → "1.2.3". Non-version specifiers (workspace:, portal:, file:,
// git URLs) are returned unchanged so callers can still see the intent.
func stripRange(rng string) string {
	return strings.TrimLeft(rng, "^~>=< ")
}
