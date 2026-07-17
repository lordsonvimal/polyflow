// Package contract_ingest implements the F.1 contract-ingestion evidence source:
// parsers for OpenAPI, protobuf, GraphQL SDL, and AsyncAPI that emit edges
// normalized to the same channel keys the static pipeline uses.
package contract_ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// httpNormsAPI is the normalizer chain for OpenAPI HTTP paths.
// Matches the producer-side chain in contracts/http.yaml minus base_url_strip
// (which is pair-conditioned and is a no-op without a link env).
var httpNormsAPI = []string{"case_fold", "url_to_path", "query_strip", "param_wildcard", "trim_slash"}

// openAPIDoc is the minimal parsed shape of an OpenAPI 3.x / Swagger 2.x document.
type openAPIDoc struct {
	OpenAPI string `yaml:"openapi"` // "3.x.y"
	Swagger string `yaml:"swagger"` // "2.0"
	Info    struct {
		Title string `yaml:"title"`
	} `yaml:"info"`
	Paths     map[string]openAPIPathItem `yaml:"paths"`
	Webhooks  map[string]interface{}     `yaml:"webhooks"`  // 3.1 — not yet supported
	Callbacks map[string]interface{}     `yaml:"callbacks"` // top-level — not yet supported
}

// openAPIPathItem holds per-method operations for one path.
type openAPIPathItem struct {
	Get     *openAPIOperation `yaml:"get"`
	Post    *openAPIOperation `yaml:"post"`
	Put     *openAPIOperation `yaml:"put"`
	Patch   *openAPIOperation `yaml:"patch"`
	Delete  *openAPIOperation `yaml:"delete"`
	Head    *openAPIOperation `yaml:"head"`
	Options *openAPIOperation `yaml:"options"`
	Trace   *openAPIOperation `yaml:"trace"`
}

// openAPIOperation is the fields we read from each operation.
type openAPIOperation struct {
	OperationID string                 `yaml:"operationId"`
	Summary     string                 `yaml:"summary"`
	Callbacks   map[string]interface{} `yaml:"callbacks"` // not yet supported
}

// parseOpenAPI reads an OpenAPI YAML/JSON file and emits Evidence edges for each
// declared operation.  Unsupported constructs (callbacks, webhooks) are ledgered.
func parseOpenAPI(path, serviceName string) (edges []graph.Edge, unres []graph.UnresolvedRef, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("openapi: read %s: %w", path, err)
	}
	var doc openAPIDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("openapi: parse %s: %w", path, err)
	}

	// Determine the display version for the ref.
	specVersion := doc.OpenAPI
	if specVersion == "" {
		specVersion = doc.Swagger
	}
	_ = specVersion

	rel := filepath.Base(path)

	// Webhooks and top-level callbacks: ledger — not yet mapped to static nodes.
	if len(doc.Webhooks) > 0 {
		unres = append(unres, graph.UnresolvedRef{
			Service: serviceName,
			File:    rel,
			Name:    "webhooks",
			Kind:    "openapi_unsupported:webhooks (F.1 does not map webhook operations; supported in a future phase)",
		})
	}
	if len(doc.Callbacks) > 0 {
		unres = append(unres, graph.UnresolvedRef{
			Service: serviceName,
			File:    rel,
			Name:    "callbacks",
			Kind:    "openapi_unsupported:callbacks (F.1 does not map top-level callbacks; supported in a future phase)",
		})
	}

	// Stable iteration order: sort path keys.
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	type methodOp struct {
		method string
		op     *openAPIOperation
	}
	methodOrder := []string{"get", "post", "put", "patch", "delete", "head", "options", "trace"}

	for _, apiPath := range paths {
		item := doc.Paths[apiPath]
		ops := []methodOp{
			{"get", item.Get},
			{"post", item.Post},
			{"put", item.Put},
			{"patch", item.Patch},
			{"delete", item.Delete},
			{"head", item.Head},
			{"options", item.Options},
			{"trace", item.Trace},
		}
		_ = methodOrder // already in the same order above

		for _, mo := range ops {
			if mo.op == nil {
				continue
			}

			// Per-operation callbacks: ledger.
			if len(mo.op.Callbacks) > 0 {
				unres = append(unres, graph.UnresolvedRef{
					Service: serviceName,
					File:    rel,
					Name:    fmt.Sprintf("%s %s callbacks", strings.ToUpper(mo.method), apiPath),
					Kind:    "openapi_unsupported:operation_callbacks (F.1 does not map operation-level callbacks; supported in a future phase)",
				})
			}

			label, normErr := contract.NormalizeFields(
				[]string{mo.method, apiPath},
				httpNormsAPI,
				contract.NormalizeEnv{},
			)
			if normErr != nil {
				return nil, nil, normErr
			}

			ref := fmt.Sprintf("%s#%s %s", rel, strings.ToUpper(mo.method), apiPath)
			if mo.op.OperationID != "" {
				ref = fmt.Sprintf("%s#%s", rel, mo.op.OperationID)
			}

			edgeID := fmt.Sprintf("contract:openapi:%s:%s", serviceName, label)
			edges = append(edges, graph.Edge{
				ID:    edgeID,
				From:  serviceName,
				To:    "",
				Type:  graph.EdgeTypeHTTPCall,
				Label: label,
				Sources: []graph.SourceRef{{
					Provider:   "contract",
					Confidence: graph.ConfidenceDeclared,
					Ref:        ref,
				}},
			})
		}
	}

	return edges, unres, nil
}
