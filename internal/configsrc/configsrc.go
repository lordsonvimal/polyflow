// Package configsrc reads environment-variable values that a service checks
// into its own repository — .env files, Kubernetes manifests and Terraform
// tfvars — and returns them keyed by variable name.
//
// It is deliberately a leaf: it knows about files and strings, not about
// graphs, edges or evidence. Two consumers need the same values for different
// reasons — the config_resolve evidence provider resolves a dynamic channel
// key from them, and the linker's config-base-URL pass reads a path prefix out
// of them — and a second dotenv parser is how the two would silently drift
// apart.
//
// Semantics inherited from the config_resolve provider (evidence-fusion-plan
// F.3), which this package was extracted from:
//
//   - Values have surrounding quotes and whitespace stripped (bug-class rule 6).
//   - A variable set to different values in different files keeps all of them,
//     in source order (fan-out, bug-class rule 1); duplicates of the same value
//     collapse to the first occurrence, which keeps the earliest ref.
//   - A missing or unreadable directory contributes nothing rather than
//     failing: absence of config is a result, not an error.
package configsrc

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Value is a config value and the file:line it came from.
type Value struct{ Value, Ref string }

// k8sSubdirs are relative subdirectory names searched for k8s manifests.
var k8sSubdirs = []string{"k8s", "kubernetes", "deploy", "deployment"}

// tfSubdirs are relative subdirectory names searched for terraform files.
var tfSubdirs = []string{"terraform", "infra", "infrastructure"}

// Load returns every environment-variable value checked in under svcPath,
// merged across .env files, k8s manifests and tfvars, keyed by variable name.
// Sources are scanned in a fixed order (dotenv, k8s, terraform) and each
// source's files sorted, so the slice for a given key is deterministic.
func Load(svcPath string) map[string][]Value {
	out := make(map[string][]Value)
	merge(out, dotenvValues(svcPath))
	for _, sub := range k8sSubdirs {
		merge(out, k8sEnvValues(filepath.Join(svcPath, sub)))
	}
	for _, sub := range tfSubdirs {
		merge(out, terraformEnvValues(filepath.Join(svcPath, sub)))
	}
	// SH2: shell deploy/entrypoint scripts (`export DATABASE_URL=...`,
	// `ENV_VAR=value some-command`) are a third real source of the same
	// facts. Unlike k8s/terraform, shell scripts carry no dedicated
	// subdirectory convention — deploy.sh/entrypoint.sh commonly sit at the
	// service root, so shellEnvValues scans the whole service tree (it
	// already recurses) rather than a fixed subdirectory list.
	merge(out, shellEnvValues(svcPath))
	return out
}

// merge adds all entries from src into dst, dropping values dst already has.
func merge(dst, src map[string][]Value) {
	for k, vals := range src {
		for _, v := range vals {
			dst[k] = appendUnique(dst[k], v)
		}
	}
}

// appendUnique appends v to out only if the same value does not already exist.
func appendUnique(out []Value, v Value) []Value {
	for _, existing := range out {
		if existing.Value == v.Value {
			return out
		}
	}
	return append(out, v)
}

// stripValue strips surrounding single/double quotes and whitespace from a raw
// config file value (bug-class rule 6: captured source text is raw).
func stripValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
	}
	// Strip inline comments (# ...) after an unquoted value.
	if idx := strings.Index(v, " #"); idx >= 0 {
		v = strings.TrimSpace(v[:idx])
	}
	return v
}

// ref builds a source provenance ref string "relPath:line".
func ref(relPath string, line int) string {
	return fmt.Sprintf("%s:%d", relPath, line)
}
