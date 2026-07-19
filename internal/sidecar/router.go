package sidecar

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/toolchain"
)

// ParseRequest is the parse-sidecar message schema (one instance of the
// payload-generic frame layer). service and datastar_variant are additive to
// the plan's pinned example: node IDs embed the service namespace and the
// datastar vocabulary is selected per service, so the sidecar cannot
// reproduce the in-process graph without them.
type ParseRequest struct {
	File            string `json:"file"`
	ContentB64      string `json:"content_b64"`
	Tool            string `json:"tool"`
	Version         string `json:"version"`
	Service         string `json:"service"`
	DatastarVariant string `json:"datastar_variant,omitempty"`
}

// ParseResponse carries the sidecar's graph output. A non-empty Error (or a
// dead/missing sidecar) triggers the in-process fallback + a coverage note —
// never an aborted run, never a dropped file.
type ParseResponse struct {
	Nodes      []graph.Node          `json:"nodes"`
	Edges      []graph.Edge          `json:"edges"`
	Unresolved []graph.UnresolvedRef `json:"unresolved"`
	Error      string                `json:"error"`
}

// Sort orders nodes/edges by ID and unresolved refs by (file, line, kind,
// name) so identical parses frame byte-identically (bug-class rule 2; the
// sidecar sorts before framing so the regression guard is testable).
func (r *ParseResponse) Sort() {
	sort.SliceStable(r.Nodes, func(i, j int) bool { return r.Nodes[i].ID < r.Nodes[j].ID })
	sort.SliceStable(r.Edges, func(i, j int) bool { return r.Edges[i].ID < r.Edges[j].ID })
	sort.SliceStable(r.Unresolved, func(i, j int) bool {
		a, b := r.Unresolved[i], r.Unresolved[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
}

// Router dispatches files whose parser engine is sidecar'd (today: .templ)
// to the pooled sidecar matching the service's resolved toolchain version.
// One Router per service per index run; safe for concurrent use by the
// parser worker pool.
type Router struct {
	manager  *Manager
	registry toolchain.Registry
	service  string
	versions map[toolchain.Tool]string

	mu        sync.Mutex
	templOnce sync.Once
	templSel  toolchain.Selection
	notes     []toolchain.CoverageNote
	noted     map[string]bool // dedup key → note already recorded
}

// NewRouter builds a per-service Router over the shared Manager.
func NewRouter(m *Manager, reg toolchain.Registry, service string, versions map[toolchain.Tool]string) *Router {
	return &Router{manager: m, registry: reg, service: service, versions: versions, noted: map[string]bool{}}
}

// ParserFor returns a sidecar-backed parser for path, or nil when the file's
// engine is not sidecar'd (the worker pool then uses the in-process
// registry). The toolchain selection is made lazily on the first routed file
// so services without any .templ file never record templ notes.
func (r *Router) ParserFor(path string) parser.Parser {
	if filepath.Ext(path) != ".templ" {
		return nil
	}
	r.templOnce.Do(func() {
		version := r.versions[toolchain.ToolTempl]
		sel := r.registry.Select(toolchain.ToolTempl, version)
		if sel.Inferred {
			requested := version
			if requested == "" {
				requested = "unresolved"
			}
			r.note("templ-inferred", toolchain.CoverageNote{
				Service:          r.service,
				Tool:             toolchain.ToolTempl,
				RequestedVersion: requested,
				UsedProfile:      sel.Backend.SidecarBackend,
				Note:             "nearest-newest fallback: no registry row satisfies the resolved templ version",
			})
		}
		r.mu.Lock()
		r.templSel = sel
		r.mu.Unlock()
	})
	r.mu.Lock()
	sel := r.templSel
	r.mu.Unlock()
	if sel.Backend.SidecarBackend == "" {
		return nil // rule-gated or unregistered: in-process registry parser
	}
	return &sidecarParser{router: r, sel: sel}
}

// note records a coverage note once per dedup key.
func (r *Router) note(key string, n toolchain.CoverageNote) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.noted[key] {
		return
	}
	r.noted[key] = true
	r.notes = append(r.notes, n)
}

// Notes returns the coverage notes recorded during routing (inferred
// selections and in-process fallbacks), in first-recorded order.
func (r *Router) Notes() []toolchain.CoverageNote {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]toolchain.CoverageNote(nil), r.notes...)
}

// sidecarParser satisfies parser.Parser by round-tripping the file through
// the pooled sidecar process; any failure falls back to the in-process
// registry parser and records a coverage note.
type sidecarParser struct {
	router *Router
	sel    toolchain.Selection
}

func (s *sidecarParser) Language() string     { return "templ" }
func (s *sidecarParser) Extensions() []string { return []string{".templ"} }

func (s *sidecarParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, nil, err
	}
	variant := ""
	if matcher != nil {
		variant = matcher.DatastarVariant
	}
	backend := s.sel.Backend.SidecarBackend
	client, err := s.router.manager.Client(backend)
	if err != nil {
		return s.fallback(file, service, matcher, backend, err, "")
	}
	req := ParseRequest{
		File:            file,
		ContentB64:      base64.StdEncoding.EncodeToString(content),
		Tool:            string(toolchain.ToolTempl),
		Version:         s.sel.Version,
		Service:         service,
		DatastarVariant: variant,
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return s.fallback(file, service, matcher, backend, err, "")
	}
	respBytes, err := client.RoundTrip(payload)
	if err != nil {
		return s.fallback(file, service, matcher, backend, err, client.Stderr())
	}
	var resp ParseResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return s.fallback(file, service, matcher, backend, err, client.Stderr())
	}
	if resp.Error != "" {
		return s.fallback(file, service, matcher, backend, fmt.Errorf("sidecar error: %s", resp.Error), client.Stderr())
	}
	return resp.Nodes, resp.Edges, resp.Unresolved, nil
}

// fallback parses in-process via the extension registry and records one
// coverage note per (backend, failure) for the run. The registry parser is
// the same engine version as this build, so output content is unchanged —
// the note records that isolation was unavailable, not a graph gap.
func (s *sidecarParser) fallback(file, service string, matcher *patterns.TreeSitterMatcher, backend string, cause error, stderr string) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	noteText := fmt.Sprintf("sidecar %s unavailable, parsed in-process: %v", backend, cause)
	if stderr != "" {
		noteText += " | stderr: " + strings.TrimSpace(stderr)
	}
	s.router.note("fallback:"+backend, toolchain.CoverageNote{
		Service:          s.router.service,
		Tool:             toolchain.ToolTempl,
		RequestedVersion: s.sel.Version,
		UsedProfile:      "in-process",
		Note:             noteText,
	})
	p := parser.ForFile(file)
	if p == nil {
		return nil, nil, nil, fmt.Errorf("sidecar fallback: no in-process parser for %s", file)
	}
	return p.Parse(file, service, matcher)
}
