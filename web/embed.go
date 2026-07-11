// Package webui embeds the built SolidJS frontend into the compiled binary so
// `polyflow serve` works from any directory, not just the source tree root.
// `make build` runs `npm run build` (vite → web/dist) before compiling the Go
// binary, so the real assets are baked in.
//
// web/dist is a build artifact (gitignored). The committed web/dist/.gitkeep
// keeps this `//go:embed` compilable on a fresh checkout where the frontend has
// not been built yet — in that state the embedded FS holds only the placeholder
// and the UI 404s until `make build` runs, but the Go code still compiles and
// the API server works.
package webui

import "embed"

// Dist holds the built frontend rooted at the `dist/` subdirectory. The `all:`
// prefix includes the dotfile placeholder so the glob always matches at least
// one file.
//
//go:embed all:dist
var Dist embed.FS
