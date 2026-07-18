// Package deps resolves the exact installed version of every dependency of a
// service, per ecosystem. Resolved versions drive version-gated pattern
// activation (a pattern written for aws-sdk-go-v2 must not run against a
// service pinned to aws-sdk-go v1) and are stored in the graph so users and
// agents can ask "what version of X does service Y use".
package deps

import (
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// Kind classifies where a dependency is declared.
const (
	KindProd = "prod" // dependencies / runtime gems / go.mod require
	KindDev  = "dev"  // devDependencies, test-only
)

// Ecosystem names.
const (
	EcosystemGo    = "go"
	EcosystemNPM   = "npm"
	EcosystemRuby  = "rubygems"
	EcosystemPyPI  = "pypi"
)

// Dependency is one resolved package version for a service.
type Dependency struct {
	Ecosystem string `json:"ecosystem"` // go | npm | rubygems
	Name      string `json:"name"`      // module path, npm name, or gem name
	Version   string `json:"version"`   // exact resolved version (no range)
	Kind      string `json:"kind"`      // prod | dev
}

// Resolve inspects dir for every supported manifest and returns all resolved
// dependencies. Missing manifests are skipped silently; parse errors on a
// present manifest are returned.
func Resolve(dir string) ([]Dependency, error) {
	var out []Dependency

	if ds, err := resolveGoMod(filepath.Join(dir, "go.mod")); err != nil {
		return nil, err
	} else {
		out = append(out, ds...)
	}

	npm, err := resolveNode(dir)
	if err != nil {
		return nil, err
	}
	out = append(out, npm...)

	if ds, err := resolveGemfileLock(filepath.Join(dir, "Gemfile.lock")); err != nil {
		return nil, err
	} else {
		out = append(out, ds...)
	}

	if ds, err := resolvePython(dir); err != nil {
		return nil, err
	} else {
		out = append(out, ds...)
	}

	return out, nil
}

// GoDirective returns the Go language version declared in the `go` directive
// of the module's go.mod (e.g. "1.25.0"). Returns "" if go.mod is absent or
// carries no directive.
func GoDirective(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	mf, err := modfile.Parse(filepath.Join(dir, "go.mod"), data, nil)
	if err != nil {
		return "", err
	}
	if mf.Go == nil {
		return "", nil
	}
	return mf.Go.Version, nil
}

// resolveGoMod reads exact required versions from go.mod. Go modules pin
// exact versions in go.mod (MVS), so no lockfile lookup is needed.
func resolveGoMod(path string) ([]Dependency, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	mf, err := modfile.Parse(path, data, nil)
	if err != nil {
		return nil, err
	}
	var out []Dependency
	for _, r := range mf.Require {
		out = append(out, Dependency{
			Ecosystem: EcosystemGo,
			Name:      r.Mod.Path,
			Version:   r.Mod.Version,
			Kind:      KindProd, // go.mod does not distinguish dev deps
		})
	}
	return out, nil
}
