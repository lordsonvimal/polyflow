// Package contractdata embeds the built-in contract rule YAML files into the
// compiled binary so polyflow can link cross-service edges without needing the
// source tree's contracts/ directory at runtime. G.1+ populates this directory.
package contractdata

import "embed"

// FS holds every built-in contract rule file. G.1 adds the first YAML rule
// files to this directory; for G.0 only the placeholder is embedded.
//
//go:embed .keep
var FS embed.FS
