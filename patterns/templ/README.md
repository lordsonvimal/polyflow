# Templ patterns

Templ (github.com/a-h/templ) files are parsed using a dedicated parser in
`internal/parser/templ.go` rather than pure tree-sitter YAML patterns, because
`.templ` files have a Go-like syntax that requires custom handling.

The templ parser extracts:
- Component definitions (`templ ComponentName(...)`)
- Component call sites (`@ComponentName(...)` inside other components)
- HTTP handler render calls (`templ.Handler(Component(...))`)

See `internal/parser/templ.go` for the implementation stub.
