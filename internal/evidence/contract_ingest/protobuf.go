package contract_ingest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

var (
	reProtoService = regexp.MustCompile(`^\s*service\s+(\w+)\s*\{`)
	reProtoRPC     = regexp.MustCompile(`^\s*rpc\s+(\w+)\s*\(`)
	reProtoPackage = regexp.MustCompile(`^\s*package\s+([\w.]+)\s*;`)
)

// parseProtobuf reads a .proto file and emits one grpc_call Evidence edge per
// RPC method. The channel key follows the standard gRPC convention:
// "/<ServiceName>/<MethodName>" — identical to what the grpc_client pattern
// stores in node meta "service_method".
func parseProtobuf(path, serviceName string) (edges []graph.Edge, _ []graph.UnresolvedRef, err error) {
	data, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("protobuf: open %s: %w", path, err)
	}
	defer data.Close()

	rel := filepath.Base(path)

	var currentService string
	scanner := bufio.NewScanner(data)
	depth := 0 // brace nesting depth while inside a service block

	for scanner.Scan() {
		line := scanner.Text()

		// Track package for context (not yet used in key, kept for future).
		if match := reProtoPackage.FindStringSubmatch(line); match != nil {
			_ = match[1]
			continue
		}

		if match := reProtoService.FindStringSubmatch(line); match != nil {
			currentService = match[1]
			depth = 1
			continue
		}

		if currentService != "" {
			for _, ch := range line {
				switch ch {
				case '{':
					depth++
				case '}':
					depth--
				}
			}
			if depth <= 0 {
				currentService = ""
				depth = 0
				continue
			}

			if match := reProtoRPC.FindStringSubmatch(line); match != nil {
				rpcName := match[1]
				// gRPC channel key: "/ServiceName/MethodName"
				key := fmt.Sprintf("/%s/%s", currentService, rpcName)
				ref := fmt.Sprintf("%s#%s.%s", rel, currentService, rpcName)
				edgeID := fmt.Sprintf("contract:proto:%s:%s", serviceName, strings.ToLower(key))
				edges = append(edges, graph.Edge{
					ID:    edgeID,
					From:  serviceName,
					To:    "",
					Type:  graph.EdgeTypeGRPCCall,
					Label: key,
					Sources: []graph.SourceRef{{
						Provider:   "contract",
						Confidence: graph.ConfidenceDeclared,
						Ref:        ref,
					}},
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("protobuf: scan %s: %w", path, err)
	}
	return edges, nil, nil
}
