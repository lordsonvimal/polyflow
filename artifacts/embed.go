// Package artifactdata embeds the built-in artifact-mapping YAML files into the
// compiled binary so `polyflow` can promote in-tree data assets to graph facts
// without the source tree's artifacts/ directory present on disk at runtime.
//
// Each mapping declares one fact kind: which file formats it applies to, the
// corroboration gate that admits an artifact, and how a qualifying artifact's
// leaves become facts. See docs/static-architecture-target-plan.md, SA.3.
package artifactdata

import "embed"

// FS holds every built-in artifact mapping. Mappings live at the top level as
// artifacts/<kind>.yaml.
//
//go:embed *.yaml
var FS embed.FS
