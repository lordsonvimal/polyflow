package patterns

import (
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/lordsonvimal/polyflow/internal/deps"
)

// ForService returns a new Registry containing only the patterns whose
// version gate is satisfied by the service's resolved dependencies.
//
//   - Patterns with no Package gate are always included.
//   - Patterns with a Package gate require the service to depend on that
//     package (any ecosystem).
//   - Patterns with a VersionRange additionally require the resolved version
//     to satisfy the range (Masterminds semver syntax).
//
// This keeps version awareness a registry capability: pattern authors gate a
// whole YAML file with `package:` + `version_range:` and the loader/matcher
// stay package-agnostic.
func (r *Registry) ForService(svcDeps []deps.Dependency) *Registry {
	versions := make(map[string]string, len(svcDeps))
	for _, d := range svcDeps {
		versions[d.Name] = d.Version
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	out := NewRegistry()
	for lang, ps := range r.patterns {
		for _, p := range ps {
			if gateSatisfied(p.Package, p.VersionRange, versions) {
				out.Register(lang, p)
			}
		}
	}
	for lang, gs := range r.reflectMethods {
		for _, g := range gs {
			if gateSatisfied(g.Package, g.VersionRange, versions) {
				out.reflectMethods[lang] = append(out.reflectMethods[lang], g)
			}
		}
	}
	return out
}

// gateSatisfied reports whether a package/version gate passes for the given
// resolved dependency versions. Shared by *Pattern and reflectMethodGate
// filtering — both carry the identical Package/VersionRange gate shape.
func gateSatisfied(pkg, versionRange string, versions map[string]string) bool {
	if pkg == "" {
		return true
	}
	version, ok := versions[pkg]
	if !ok {
		return false
	}
	if versionRange == "" {
		return true
	}
	return VersionInRange(version, versionRange)
}

// VersionInRange reports whether an exact version satisfies a range
// expression. Unparseable versions (git refs, workspace:/portal: specifiers,
// Go pseudo-versions are fine) fail closed — a pattern gated on a version
// range must not activate when the version is unknowable.
func VersionInRange(version, rangeExpr string) bool {
	c, err := semver.NewConstraint(rangeExpr)
	if err != nil {
		return false
	}
	v, err := semver.NewVersion(strings.TrimPrefix(version, "v"))
	if err != nil {
		return false
	}
	return c.Check(v)
}

// ResolvedVersions extracts a package→version map from a dependency list,
// for stamping match metadata.
func ResolvedVersions(svcDeps []deps.Dependency) map[string]string {
	out := make(map[string]string, len(svcDeps))
	for _, d := range svcDeps {
		out[d.Name] = d.Version
	}
	return out
}
