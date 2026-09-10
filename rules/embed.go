// Package rules embeds the Tier DL rule files.
//
// They are embedded rather than read from disk for the same reason the layer
// contract is a test rather than a convention: a rule file the binary cannot
// find degrades to "this relation is empty", which is indistinguishable from
// "this codebase has no filters". A missing embed is a build failure.
//
// The directory layout is one subdirectory per language, mirroring patterns/.
package rules

import (
	"embed"
	"fmt"
)

//go:embed */*.dl
var files embed.FS

// FS is the embedded rule tree, one subdirectory per language. The FX.6
// pipeline (internal/factpipe) walks it to pair each `rules/<lang>/<name>.dl`
// with `patterns/<lang>/<name>.yaml`.
var FS = files

// Load returns one rule file by its path under rules/, e.g. "ruby/rails_filters.dl".
func Load(name string) ([]byte, error) {
	b, err := files.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("rules: %s is not embedded: %w", name, err)
	}
	return b, nil
}

// MustLoad is Load for package-level initialization.
func MustLoad(name string) []byte {
	b, err := Load(name)
	if err != nil {
		panic(err)
	}
	return b
}
