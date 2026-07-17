// Package contract_ingest is the F.1 evidence provider that reads IDL/spec files
// (OpenAPI, protobuf, GraphQL SDL, AsyncAPI) and emits contract-sourced edges
// normalized to the same channel keys the static pipeline uses.
//
// Discovery: for each service in the workspace, the provider globs for known
// spec filenames. Callers can override via WorkspaceConfig.Evidence.ContractGlobs.
package contract_ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// defaultContractGlobs are the filenames searched under each service path when
// no ContractGlobs are configured.  Deterministic order: most-specific first.
var defaultContractGlobs = []string{
	"**/openapi.yaml",
	"**/openapi.yml",
	"**/openapi.json",
	"**/swagger.yaml",
	"**/swagger.yml",
	"**/swagger.json",
	"**/*.proto",
	"**/*.graphql",
	"**/*.graphqls",
	"**/asyncapi.yaml",
	"**/asyncapi.yml",
}

// ContractProvider implements evidence.Provider for IDL/spec files.
// Name returns "contract"; graceful degradation on missing/empty specs.
type ContractProvider struct{}

// NewContractProvider creates a ContractProvider.  No configuration is needed
// at construction time — all inputs come from WorkspaceConfig in Collect.
func NewContractProvider() *ContractProvider {
	return &ContractProvider{}
}

func (p *ContractProvider) Name() string { return "contract" }

// Collect discovers spec files across all workspace services, parses each, and
// returns Evidence normalized to contract-engine channel keys.
//
// A service with no spec files produces no edges (graceful degradation).
// Spec constructs the parser does not yet support are ledgered, never silently
// skipped (bug-class rule 3).
//
// Output is deterministic: files are processed in sorted order per service,
// services in workspace order; edge IDs are derived from content, not counters
// (bug-class rule 2).
func (p *ContractProvider) Collect(_ context.Context, ws *workspace.WorkspaceConfig) (evidence.Evidence, error) {
	if ws == nil {
		return evidence.Evidence{}, nil
	}

	globs := ws.Evidence.ContractGlobs
	if len(globs) == 0 {
		globs = defaultContractGlobs
	}

	// Honor the same exclude set the indexer's file walk uses, so spec
	// discovery never ingests third-party specs from node_modules/vendor
	// that the static pass would never see.
	excludes := append(append([]string{}, ws.Index.Exclude...), workspace.LoadIgnoreFile(".")...)

	var allEdges []graph.Edge
	var allUnresolved []graph.UnresolvedRef
	seen := make(map[string]bool) // dedup edges by ID

	for _, svc := range ws.Services {
		svcPath, err := filepath.Abs(svc.Path)
		if err != nil {
			svcPath = svc.Path
		}

		// Discover spec files matching the globs.
		specFiles, err := discoverSpecs(svcPath, globs, excludes)
		if err != nil {
			return evidence.Evidence{}, fmt.Errorf("contract_ingest: discover specs for %s: %w", svc.Name, err)
		}

		for _, specPath := range specFiles {
			edges, unres, parseErr := parseSpecFile(specPath, svc.Name)
			if parseErr != nil {
				// A parse error is ledgered, not a hard failure — graceful degradation.
				allUnresolved = append(allUnresolved, graph.UnresolvedRef{
					Service: svc.Name,
					File:    specPath,
					Name:    filepath.Base(specPath),
					Kind:    fmt.Sprintf("contract_parse_error:%s", parseErr.Error()),
				})
				continue
			}
			for _, e := range edges {
				if seen[e.ID] {
					continue
				}
				seen[e.ID] = true
				allEdges = append(allEdges, e)
			}
			allUnresolved = append(allUnresolved, unres...)
		}
	}

	// Sort edges by ID for deterministic output (bug-class rule 2).
	sort.Slice(allEdges, func(i, j int) bool {
		return allEdges[i].ID < allEdges[j].ID
	})

	return evidence.Evidence{
		Edges:      allEdges,
		Unresolved: allUnresolved,
	}, nil
}

// discoverSpecs returns the set of spec files under root that match any of the
// given doublestar globs and none of the exclude globs, sorted for
// deterministic order. Globs are matched against the DirFS rooted at root;
// returned paths are absolute (root joined with the matched relative path).
// Exclude patterns match against the root-relative path, the same semantics
// as the indexer's walkService.
func discoverSpecs(root string, globs, excludes []string) ([]string, error) {
	fsys := os.DirFS(root)
	seen := make(map[string]bool)
	var files []string

	for _, glob := range globs {
		// Convert OS path globs to forward-slash form for io/fs.
		fsGlob := filepath.ToSlash(glob)
		matches, err := doublestar.Glob(fsys, fsGlob)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			if excludedPath(m, excludes) {
				continue
			}
			abs := filepath.Join(root, m)
			if !seen[abs] {
				seen[abs] = true
				files = append(files, abs)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// excludedPath reports whether the root-relative slash path rel matches any
// exclude glob.
func excludedPath(rel string, excludes []string) bool {
	for _, pattern := range excludes {
		if matched, _ := doublestar.Match(filepath.ToSlash(pattern), rel); matched {
			return true
		}
	}
	return false
}

// parseSpecFile dispatches to the appropriate parser based on file extension and
// (for YAML/JSON) the document's root keys.
func parseSpecFile(path, serviceName string) ([]graph.Edge, []graph.UnresolvedRef, error) {
	name := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(name, ".proto"):
		return parseProtobuf(path, serviceName)
	case strings.HasSuffix(name, ".graphql") || strings.HasSuffix(name, ".graphqls"):
		return parseGraphQL(path, serviceName)
	default:
		// For YAML/JSON, sniff the root keys to distinguish AsyncAPI from OpenAPI.
		if sniffAsyncAPI(path) {
			return parseAsyncAPI(path, serviceName)
		}
		return parseOpenAPI(path, serviceName)
	}
}

// sniffAsyncAPI reads the first 512 bytes of a YAML/JSON file to check for
// the AsyncAPI marker.  This avoids full YAML parsing for what is usually a
// quick filename-based dispatch in production (asyncapi.yaml), while still
// handling test fixtures named "input.yaml".
func sniffAsyncAPI(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return strings.Contains(string(buf[:n]), "asyncapi:")
}
