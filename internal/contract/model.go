// Package contract implements the generic contract-matching engine.
// A contract is a normalised cross-boundary connection between a producer
// node (emitting a channel key) and a consumer node (receiving on that key).
// Rules are declared in YAML and loaded at startup; the engine is pure Go
// and has no knowledge of individual protocols.
package contract

import (
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// Kind is the protocol or framework that defines the channel.
type Kind string

const (
	KindHTTP        Kind = "http"
	KindAMQP        Kind = "amqp"
	KindKafka       Kind = "kafka"
	KindNATS        Kind = "nats"
	KindRedisPubSub Kind = "redis_pubsub"
	KindSSE         Kind = "sse"
	KindWebSocket   Kind = "websocket"
	KindJob         Kind = "job"
	KindHub         Kind = "hub"
	KindPusher      Kind = "pusher"
	KindGRPC        Kind = "grpc"
	KindGraphQL     Kind = "graphql"
)

// Role is the node's side of a contract channel.
type Role string

const (
	RoleProducer Role = "producer"
	RoleConsumer Role = "consumer"
)

// Contract is one node projected onto a channel.
type Contract struct {
	Kind    Kind
	Role    Role
	Key     string // normalized channel key, e.g. "GET /games/*"
	RawKey  string // pre-normalization key (exact tier + diagnostics)
	Service string
	NodeID  string
}

// Normalizer transforms one key-field value. Pure function of (value, env):
// it must NOT read other nodes — contextual enrichment happens before the
// engine (G.3's meta-enrichment pass).
type Normalizer func(value string, env NormalizeEnv) string

// NormalizeEnv is the only context a normalizer may condition on. This is
// how pair-conditioned transforms (base_url_strip) work without breaking
// purity: the engine evaluates consumer keys per (FromService, ToService).
type NormalizeEnv struct {
	FromService string
	ToService   string
	Links       []workspace.Link // hints: base_url, target_service, via/exchange
}

// MatchTier controls how the engine matches producer keys to consumer keys.
type MatchTier string

const (
	TierExact            MatchTier = "exact"             // hash join on RawKey
	TierNormalized       MatchTier = "normalized"        // hash join on Key
	TierWildcardAnchored MatchTier = "wildcard_anchored" // segment match; ≥1 shared concrete segment required
)

// UnmatchedPolicy controls what happens when a producer finds no consumer.
type UnmatchedPolicy string

const (
	UnmatchedUnknownEdge UnmatchedPolicy = "unknown_edge" // edge → unresolved:<svc>
	UnmatchedLedger      UnmatchedPolicy = "ledger"       // graph.UnresolvedRef only
	UnmatchedDrop        UnmatchedPolicy = "drop"         // discard (nav-links)
)

// Rule is the YAML-mapped shape for a contract rule file entry.
type Rule struct {
	Kind         Kind            `yaml:"kind"`
	Package      string          `yaml:"package,omitempty"`       // semver gate — reserved; Load rejects until enforced (V.4)
	VersionRange string          `yaml:"version_range,omitempty"` // patterns.VersionInRange — reserved; Load rejects until enforced
	Producer     EndpointSpec    `yaml:"producer"`
	Consumer     EndpointSpec    `yaml:"consumer"`
	Normalizers  []string        `yaml:"normalizers"`
	Match        []MatchTier     `yaml:"match"`
	Edge         EdgeSpec        `yaml:"edge"`
	Unmatched    UnmatchedPolicy `yaml:"unmatched"`
}

// EndpointSpec describes how to identify and key producer or consumer nodes.
type EndpointSpec struct {
	Node              graph.NodeType      `yaml:"node"`
	Where             map[string]string   `yaml:"where,omitempty"`         // meta equality; "" ⇒ absent/empty
	Key               []string            `yaml:"key"`                     // meta fields, joined with " "
	KeyFallbacks      map[string][]string `yaml:"key_fallbacks,omitempty"` // per-field meta fallbacks
	MethodFallback    []string            `yaml:"method_fallback,omitempty"`
	TargetServiceMeta string              `yaml:"target_service_meta,omitempty"` // producer meta key → restrict to that service
}

// EdgeSpec describes the edge emitted on a successful match.
type EdgeSpec struct {
	Type        graph.EdgeType    `yaml:"type"`
	IDPrefix    string            `yaml:"id_prefix"`    // edge ID "<prefix>:<from>-><to>" — part of parity
	SameService string            `yaml:"same_service"` // "skip" | "keep" | "skip_unless_meta:<key>"
	ViaMeta     map[string]string `yaml:"via_meta,omitempty"` // producer meta key → Meta["via"] value
}

// Engine runs the contract matching engine.
type Engine struct{}

// Result is the output of Engine.Link.
type Result struct {
	Edges      []graph.Edge
	Nodes      []graph.Node          // synthetic targets (unresolved:<svc>)
	Unresolved []graph.UnresolvedRef // one per UnmatchedLedger miss
}
