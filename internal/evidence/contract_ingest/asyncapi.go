package contract_ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// asyncAPIDoc is the minimal parsed shape of an AsyncAPI 2.x document.
// AsyncAPI 3.x restructures channels/operations; 2.x is supported here.
type asyncAPIDoc struct {
	AsyncAPI string `yaml:"asyncapi"`
	Info     struct {
		Title string `yaml:"title"`
	} `yaml:"info"`
	Channels map[string]asyncAPIChannel `yaml:"channels"`
}

// asyncAPIChannel represents one channel entry.
type asyncAPIChannel struct {
	Publish   *asyncAPIOperation      `yaml:"publish"`
	Subscribe *asyncAPIOperation      `yaml:"subscribe"`
	Bindings  map[string]asyncBinding `yaml:"bindings"`
}

type asyncAPIOperation struct {
	OperationID string                 `yaml:"operationId"`
	Bindings    map[string]interface{} `yaml:"bindings"`
}

// asyncBinding holds protocol-specific binding details.
type asyncBinding struct {
	Topic   string `yaml:"topic"`   // Kafka topic override
	Subject string `yaml:"subject"` // NATS subject override
	Queue   string `yaml:"queue"`   // AMQP queue override
}

// parseAsyncAPI reads an AsyncAPI 2.x YAML file and emits one Evidence edge
// per publish/subscribe operation.  The channel key is the AsyncAPI channel
// name (or the protocol-specific topic override when present).  Edge types map
// by protocol binding: kafka→kafka_publish, nats→nats_publish, default→publishes/subscribes.
func parseAsyncAPI(path, serviceName string) (edges []graph.Edge, _ []graph.UnresolvedRef, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("asyncapi: read %s: %w", path, err)
	}
	var doc asyncAPIDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("asyncapi: parse %s: %w", path, err)
	}

	rel := filepath.Base(path)

	// Stable iteration order.
	channelNames := make([]string, 0, len(doc.Channels))
	for name := range doc.Channels {
		channelNames = append(channelNames, name)
	}
	sort.Strings(channelNames)

	for _, chanName := range channelNames {
		ch := doc.Channels[chanName]

		// Determine the protocol from bindings (kafka > nats > amqp > default).
		proto := protocolFromBindings(ch.Bindings)

		// Resolve the channel key: prefer protocol-specific topic/subject,
		// fall back to the channel name.
		key := resolveChannelKey(chanName, proto, ch.Bindings)

		type opEntry struct {
			dir string
			op  *asyncAPIOperation
		}
		for _, oe := range []opEntry{{"publish", ch.Publish}, {"subscribe", ch.Subscribe}} {
			if oe.op == nil {
				continue
			}

			edgeType := asyncEdgeType(proto, oe.dir)
			ref := fmt.Sprintf("%s#%s:%s", rel, chanName, oe.dir)
			if oe.op.OperationID != "" {
				ref = fmt.Sprintf("%s#%s", rel, oe.op.OperationID)
			}
			edgeID := fmt.Sprintf("contract:asyncapi:%s:%s:%s", serviceName, oe.dir, strings.ReplaceAll(key, "/", "_"))
			edges = append(edges, graph.Edge{
				ID:    edgeID,
				From:  serviceName,
				To:    "",
				Type:  edgeType,
				Label: key,
				Sources: []graph.SourceRef{{
					Provider:   "contract",
					Confidence: graph.ConfidenceDeclared,
					Ref:        ref,
				}},
			})
		}
	}
	return edges, nil, nil
}

func protocolFromBindings(bindings map[string]asyncBinding) string {
	for _, proto := range []string{"kafka", "nats", "amqp", "mqtt", "redis"} {
		if _, ok := bindings[proto]; ok {
			return proto
		}
	}
	return ""
}

func resolveChannelKey(chanName, proto string, bindings map[string]asyncBinding) string {
	if b, ok := bindings[proto]; ok {
		switch proto {
		case "kafka":
			if b.Topic != "" {
				return b.Topic
			}
		case "nats":
			if b.Subject != "" {
				return b.Subject
			}
		case "amqp":
			if b.Queue != "" {
				return b.Queue
			}
		}
	}
	return chanName
}

func asyncEdgeType(proto, dir string) graph.EdgeType {
	switch proto {
	case "kafka":
		return graph.EdgeTypeKafkaPublish
	case "nats":
		return graph.EdgeTypeNATSPublish
	case "redis":
		return graph.EdgeTypeRedisPublish
	}
	// Default: use generic publish/subscribe distinction.
	if dir == "subscribe" {
		return graph.EdgeTypeSubscribes
	}
	return graph.EdgeTypePublishes
}
