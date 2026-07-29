package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// amqpModule lays out a fake amqp091-go Channel plus a two-hop publish wrapper
// (SendConfig → Manager.Publish(exchange,…) → ch.PublishWithContext), a direct
// literal publish, a direct QueueBind consumer, and a dynamic-exchange publish
// whose exchange is a bare parameter (must NOT resolve). The import path contains
// "amqp" so amqpPublishArgs/amqpBindArgs recognize the calls.
func amqpModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/amqptest\n\ngo 1.25.0\n",
		"amqp/amqp.go": `package amqp

type Channel struct{}

func (c *Channel) Publish(exchange, key string, mandatory, immediate bool, msg []byte) error {
	return nil
}
func (c *Channel) PublishWithContext(ctx interface{}, exchange, key string, mandatory, immediate bool, msg []byte) error {
	return nil
}
func (c *Channel) QueueBind(name, key, exchange string, noWait bool, args interface{}) error {
	return nil
}
`,
		// main.go — line numbers are asserted below; keep this block stable.
		"main.go": `package main

import "example.com/amqptest/amqp"

type Manager struct{ ch *amqp.Channel }

func (m *Manager) Publish(exchange, key string, body []byte) error {
	return m.ch.PublishWithContext(nil, exchange, key, false, false, body)
}

func (m *Manager) SendConfig() error {
	return m.Publish("shinyproxy_config", "", nil)
}

func directPublish(ch *amqp.Channel) {
	ch.Publish("build_logs", "rk", false, false, nil)
}

func directBind(ch *amqp.Channel) {
	ch.QueueBind("qn", "bk", "bind_ex", false, nil)
}

func dynamicPublish(ch *amqp.Channel, ex string) {
	ch.Publish(ex, "", false, false, nil)
}

func main() {}
`,
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestAMQPNames_WrapperConstAndDirect locks Tier W.2: a two-hop wrapper publish
// with a literal exchange resolves to a producer channel; a direct literal
// publish and a direct QueueBind resolve to producer/consumer channels; a
// parameter-exchange publish with no literal caller resolves to nothing (the
// honest dynamic ledger stands). Channel IDs are byte-identical to the matcher's
// Pass-4 keying so the amqp contract joins them cross-service.
func TestAMQPNames_WrapperConstAndDirect(t *testing.T) {
	dir := amqpModule(t)
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:method:Publish:7":           true,
		"svc:main.go:method:SendConfig:11":       true,
		"svc:main.go:function:directPublish:15":  true,
		"svc:main.go:function:directBind:19":     true,
		"svc:main.go:function:dynamicPublish:23": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	channels := map[string]graph.Node{} // label → node
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeChannel && n.Meta["synthesized"] == "amqp_wrapper" {
			channels[n.Label] = n
		}
	}

	wantChannels := []string{"shinyproxy_config/", "build_logs/rk", "bind_ex/bk"}
	for _, want := range wantChannels {
		n, ok := channels[want]
		if !ok {
			t.Fatalf("missing synth channel %q; got %v", want, keysOf(channels))
		}
		if n.ID != "svc:channel:"+want {
			t.Errorf("channel %q: ID=%q, want svc:channel:%s", want, n.ID, want)
		}
	}
	// The dynamic-exchange publish must NOT resolve.
	if len(channels) != len(wantChannels) {
		t.Errorf("expected exactly %d synth channels, got %d: %v", len(wantChannels), len(channels), keysOf(channels))
	}

	// Edge directions: publishes caller→channel for producers, subscribes
	// channel→caller for the consumer.
	var pub, sub int
	for _, e := range res.Edges {
		if e.Meta["via"] != "amqp_wrapper" {
			continue
		}
		switch e.Type {
		case graph.EdgeTypePublishes:
			pub++
			if !known[e.From] {
				t.Errorf("publishes edge from unknown node %q", e.From)
			}
		case graph.EdgeTypeSubscribes:
			sub++
			if !known[e.To] {
				t.Errorf("subscribes edge to unknown node %q", e.To)
			}
		}
	}
	if pub != 2 {
		t.Errorf("expected 2 publishes edges (SendConfig, directPublish), got %d", pub)
	}
	if sub != 1 {
		t.Errorf("expected 1 subscribes edge (directBind), got %d", sub)
	}
}

func keysOf(m map[string]graph.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
