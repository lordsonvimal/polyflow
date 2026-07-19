package toolchain

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/deps"
)

// ResolveToolchain maps each Tool to the version string resolved from the
// target project at svcDir. svcDeps is the already-resolved dependency list
// from deps.Resolve.
//
// Resolution rules per tool:
//   - go:         go directive from go.mod, e.g. "1.25.0"
//   - typescript: "typescript" npm dep version
//   - templ:      "github.com/a-h/templ" go dep (v-prefix stripped)
//   - datastar:   "github.com/starfederation/datastar-go" go dep (preferred),
//                 or "@starfederation/datastar" npm dep (v-prefix stripped)
//   - javascript: constant "living" (no per-project runtime version resolved)
//   - html:       constant "living" (stable standard)
//   - ruby:       first non-empty line of .ruby-version (ruby- prefix stripped)
func ResolveToolchain(svcDir string, svcDeps []deps.Dependency) map[Tool]string {
	goDeps := make(map[string]string, len(svcDeps))
	npmDeps := make(map[string]string, len(svcDeps))
	for _, d := range svcDeps {
		switch d.Ecosystem {
		case deps.EcosystemGo:
			goDeps[d.Name] = d.Version
		case deps.EcosystemNPM:
			npmDeps[d.Name] = d.Version
		}
	}

	out := make(map[Tool]string)

	if goVer, err := deps.GoDirective(svcDir); err == nil && goVer != "" {
		out[ToolGo] = goVer
	}

	if v, ok := npmDeps["typescript"]; ok && v != "" {
		out[ToolTypeScript] = strings.TrimPrefix(v, "v")
	}

	if v, ok := goDeps["github.com/a-h/templ"]; ok && v != "" {
		out[ToolTempl] = strings.TrimPrefix(v, "v")
	}

	if v, ok := goDeps["github.com/starfederation/datastar-go"]; ok && v != "" {
		out[ToolDatastar] = strings.TrimPrefix(v, "v")
	} else if v, ok := npmDeps["@starfederation/datastar"]; ok && v != "" {
		out[ToolDatastar] = strings.TrimPrefix(v, "v")
	}

	if rb := readRubyVersion(filepath.Join(svcDir, ".ruby-version")); rb != "" {
		out[ToolRuby] = rb
	}

	// Living/stable constants — always present.
	out[ToolHTML] = "living"
	out[ToolJavaScript] = "living"

	return out
}

// CoverageNote records a tool version selection that required the nearest-
// newest fallback (Inferred=true). Accumulated by SelectAll and persisted
// in graph meta as "toolchain_coverage"; surfaced by `polyflow doctor` in V.4.
type CoverageNote struct {
	Service          string `json:"service"`
	Tool             Tool   `json:"tool"`
	RequestedVersion string `json:"requested_version"`
	UsedProfile      string `json:"used_profile"` // RuleVariant or SidecarBackend of the fallback
	// Note carries free-text detail for sidecar-router records (V.2):
	// fallback cause, captured stderr. Empty for plain inferred selections.
	Note string `json:"note,omitempty"`
}

// SelectAll runs Select for every resolved tool version and returns all
// selections plus coverage notes for any inferred (nearest-newest) fallbacks.
func SelectAll(reg Registry, svcName string, versions map[Tool]string) ([]Selection, []CoverageNote) {
	selections := make([]Selection, 0, len(versions))
	var notes []CoverageNote
	for tool, version := range versions {
		sel := reg.Select(tool, version)
		selections = append(selections, sel)
		if sel.Inferred {
			profile := sel.Backend.RuleVariant
			if profile == "" {
				profile = sel.Backend.SidecarBackend
			}
			notes = append(notes, CoverageNote{
				Service:          svcName,
				Tool:             tool,
				RequestedVersion: version,
				UsedProfile:      profile,
			})
		}
	}
	return selections, notes
}

// readRubyVersion reads and normalises the content of a .ruby-version file.
func readRubyVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	return strings.TrimPrefix(line, "ruby-")
}
