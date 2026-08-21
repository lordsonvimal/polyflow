package graph

import (
	"fmt"
	"sort"
)

// ServiceChannel is one concrete channel crossing a service pair — a group
// of edges sharing the same channel identity, all running the same
// direction between the two requested services. From/To name that
// direction explicitly (rather than always matching the request's own
// from/to) since ServiceChannels now answers for the pair in either
// direction — a single overview edge can represent traffic both ways.
type ServiceChannel struct {
	Kind              string `json:"kind"`
	Channel           string `json:"channel"`
	EdgeID            string `json:"edge_id"`
	From              string `json:"from"`
	To                string `json:"to"`
	VerificationState string `json:"verification_state,omitempty"`
	ProducerCount     int    `json:"producer_count"`
	ConsumerCount     int    `json:"consumer_count"`
}

// ServiceChannelsResult is the response body for GET /api/services/channels.
type ServiceChannelsResult struct {
	From     string           `json:"from"`
	To       string           `json:"to"`
	Channels []ServiceChannel `json:"channels"`
}

// verificationRank orders states worst-first (rank 0), mirroring the
// frontend's VERIFICATION_RANK in views/canvas/scopes/flow.ts — the two
// must agree since both decide "worst state wins" for a group summary.
var verificationRank = map[string]int{
	StateObservedOnlyGap: 0,
	StateConflicting:     1,
	StateCandidate:       2,
	StateVerified:        3,
}

func worseVerificationState(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	ra, ok := verificationRank[a]
	if !ok {
		ra = verificationRank[StateVerified]
	}
	rb, ok := verificationRank[b]
	if !ok {
		rb = verificationRank[StateVerified]
	}
	if rb < ra {
		return b
	}
	return a
}

// channelIdentity derives e's channel-grouping key using the same
// tiered priority as Seam (channel_key meta, else an adjacent
// NodeTypeChannel node's label, else the edge's own label/type) so a
// channel drilled into here resolves to the identical seam Seam() would
// find from any of its member edges.
func channelIdentity(idx *AdjacencyIndex, e *Edge) string {
	if key := e.Meta["channel_key"]; key != "" {
		return key
	}
	if fn := idx.Nodes[e.From]; fn != nil && fn.Type == NodeTypeChannel {
		return channelLabel(fn)
	}
	if tn := idx.Nodes[e.To]; tn != nil && tn.Type == NodeTypeChannel {
		return channelLabel(tn)
	}
	if method, path := e.Meta["method"], e.Meta["path"]; method != "" || path != "" {
		return method + " " + path
	}
	if e.Label != "" {
		return e.Label
	}
	return string(e.Type)
}

// ServiceChannels lists every concrete channel crossing the (from, to)
// service pair in *either* direction — the drill-in target for a
// single-click on an aggregated overview service-pair edge (UF.3/UN.8).
// The overview now draws one edge per unordered pair regardless of how many
// directions/types cross it (lib/aggregate.ts's aggregateServices), so this
// answers for both directions and each returned channel names its own
// direction explicitly (Kind/From/To), rather than assuming every row runs
// the same way as the request's own from/to. Includes channel-node-mediated
// pub/sub edges, since a NodeTypeChannel node's Service is the publisher's
// service by construction (see flows_test.go), making a channel->consumer
// edge already cross the pair directly without extra channel-node hopping
// here.
func ServiceChannels(idx *AdjacencyIndex, from, to string) (*ServiceChannelsResult, error) {
	if from == "" || to == "" {
		return nil, fmt.Errorf("missing from/to service")
	}

	type group struct {
		kind      string
		channel   string
		edgeID    string
		from      string
		to        string
		state     string
		producers map[string]bool
		consumers map[string]bool
	}
	groups := map[string]*group{}

	for _, edge := range idx.AllEdges() {
		e := &edge
		if !isFlowEdge(e.Type) {
			continue
		}
		fn, tn := idx.Nodes[e.From], idx.Nodes[e.To]
		if fn == nil || tn == nil {
			continue
		}
		var dirFrom, dirTo string
		switch {
		case fn.Service == from && tn.Service == to:
			dirFrom, dirTo = from, to
		case fn.Service == to && tn.Service == from:
			dirFrom, dirTo = to, from
		default:
			continue
		}
		identity := channelIdentity(idx, e)
		key := dirFrom + "\x00" + dirTo + "\x00" + string(e.Type) + "\x00" + identity
		g := groups[key]
		if g == nil {
			g = &group{kind: string(e.Type), channel: identity, edgeID: e.ID, from: dirFrom, to: dirTo, producers: map[string]bool{}, consumers: map[string]bool{}}
			groups[key] = g
		}
		if e.ID < g.edgeID {
			g.edgeID = e.ID
		}
		g.state = worseVerificationState(g.state, e.VerificationState)
		g.producers[e.From] = true
		g.consumers[e.To] = true
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	channels := make([]ServiceChannel, 0, len(keys))
	for _, k := range keys {
		g := groups[k]
		channels = append(channels, ServiceChannel{
			Kind:              g.kind,
			Channel:           g.channel,
			EdgeID:            g.edgeID,
			From:              g.from,
			To:                g.to,
			VerificationState: g.state,
			ProducerCount:     len(g.producers),
			ConsumerCount:     len(g.consumers),
		})
	}

	return &ServiceChannelsResult{From: from, To: to, Channels: channels}, nil
}
