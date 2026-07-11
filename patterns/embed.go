// Package patterndata embeds the built-in tree-sitter pattern YAML files into
// the compiled binary so `polyflow` can index any repository without needing
// the source tree's patterns/ directory present on disk at runtime.
package patterndata

import "embed"

// FS holds every built-in pattern file. All patterns live one level deep as
// patterns/<language>/<name>.yaml; the fixture directories (…_test/) contain no
// .yaml files, so this glob captures exactly the pattern definitions.
//
//go:embed */*.yaml
var FS embed.FS
