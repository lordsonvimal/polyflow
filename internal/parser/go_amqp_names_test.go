package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"sort"
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

// bindTableModule reproduces the maple-manager binding chain J.1 targets:
// a static `Queues()` declaration table, a `declareQueues(d queueDeclarer)` loop
// that binds `(q.Name, routingKey, q.Exchange)` through an *interface*, an
// adapter forwarding to the client, and the client's own QueueBind wrapper over
// amqp091's channel.QueueBind. The exchange is never a constant at any call site.
func bindTableModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/bindtest\n\ngo 1.25.0\n",
		"amqp/amqp.go": `package amqp

type Channel struct{}

func (c *Channel) QueueBind(name, key, exchange string, noWait bool, args interface{}) error {
	return nil
}
func (c *Channel) Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args interface{}) (<-chan int, error) {
	return nil, nil
}
`,
		"main.go": `package main

import "example.com/bindtest/amqp"

const (
	QueueBuildLogs      = "build_logs_queue"
	ExchangeBuildLogs   = "build_logs"
	QueueContainerEvts  = "container_events_queue"
	ExchangeContainerEv = "container_events"
)

type QueueDecl struct {
	Name        string
	Durable     bool
	Exchange    string
	RoutingKeys []string
}

func Queues() []QueueDecl {
	return []QueueDecl{
		{
			Name: QueueBuildLogs, Durable: true, Exchange: ExchangeBuildLogs,
			RoutingKeys: []string{"logs.build.*", "logs.workflow.*"},
		},
		{
			Name: QueueContainerEvts, Durable: true, Exchange: ExchangeContainerEv,
			RoutingKeys: []string{"container.#"},
		},
	}
}

type queueDeclarer interface {
	BindQueue(queueName, routingKey, exchangeName string) error
}

type Client struct{ ch *amqp.Channel }

func (c *Client) BindQueue(queueName, routingKey, exchangeName string) error {
	return c.ch.QueueBind(queueName, routingKey, exchangeName, false, nil)
}

type adapter struct{ c *Client }

func (a adapter) BindQueue(queueName, routingKey, exchangeName string) error {
	return a.c.BindQueue(queueName, routingKey, exchangeName)
}

func declareQueues(d queueDeclarer) error {
	for _, q := range Queues() {
		for _, routingKey := range q.RoutingKeys {
			if err := d.BindQueue(q.Name, routingKey, q.Exchange); err != nil {
				return err
			}
		}
	}
	return nil
}

func setupQueues(c *Client) error {
	return declareQueues(adapter{c})
}

func (c *Client) Consume(queueName, consumerName string) error {
	_, err := c.ch.Consume(queueName, consumerName, false, false, false, false, nil)
	return err
}

func consumeBuildLogs(c *Client) error {
	return c.Consume(QueueBuildLogs, "build_logs_consumer")
}

func consumeDynamic(c *Client, q string) error {
	return c.Consume(q, "dynamic_consumer")
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

// bindTableKnownNodes is the tree-sitter node set bindTableModule's functions
// resolve against; line numbers track the fixture above.
func bindTableKnownNodes() map[string]bool {
	return map[string]bool{
		"svc:main.go:method:BindQueue:38":          true,
		"svc:main.go:method:BindQueue:44":          true,
		"svc:main.go:function:declareQueues:48":    true,
		"svc:main.go:function:setupQueues:59":      true,
		"svc:main.go:method:Consume:63":            true,
		"svc:main.go:function:consumeBuildLogs:68": true,
		"svc:main.go:function:consumeDynamic:72":   true,
	}
}

// TestExtractAMQPNames_QueueConsumerJoinsTableChannels locks the second half of
// J.1: a consumer that names only its queue (`Consume(QueueBuildLogs, …)`,
// through a wrapper) subscribes to every channel that queue is bound to by the
// static table — the case that otherwise leaves an exchange-less `dynamic`
// subscriber. A consumer whose queue name is a variable resolves to nothing.
func TestExtractAMQPNames_QueueConsumerJoinsTableChannels(t *testing.T) {
	dir := bindTableModule(t)
	t.Chdir(dir)

	known := bindTableKnownNodes()
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	got := map[string]bool{}
	for _, e := range res.Edges {
		if e.Meta["via"] != "amqp_queue_table" {
			continue
		}
		if e.Type != graph.EdgeTypeSubscribes {
			t.Errorf("edge %s: type=%s, want subscribes", e.ID, e.Type)
		}
		if e.Confidence != graph.ConfidenceStatic {
			t.Errorf("edge %s: confidence=%s, want static", e.ID, e.Confidence)
		}
		if e.Meta["queue_name"] != "build_logs_queue" {
			t.Errorf("edge %s: queue_name=%q", e.ID, e.Meta["queue_name"])
		}
		got[e.From+"->"+e.To] = true
	}

	consumer := "svc:main.go:function:consumeBuildLogs:68"
	for _, key := range []string{"logs.build.*", "logs.workflow.*"} {
		want := "svc:channel:build_logs/" + key + "->" + consumer
		if !got[want] {
			t.Errorf("missing subscribes edge %s (got %v)", want, got)
		}
	}
	// The queue binds exactly two keys here, and the dynamic-queue consumer
	// contributes nothing: never fan a consumer out over unrelated queues.
	if len(got) != 2 {
		t.Errorf("got %d queue-table subscribes edges, want 2: %v", len(got), got)
	}
}

// TestExtractAMQPNames_BindViaStaticTable locks J.1: a bind whose exchange is a
// struct-field load off a static declaration table mints one consumer channel per
// (row × routing key), carrying the declaring row's own provenance, with a
// deterministic (ID-sorted) output order.
func TestExtractAMQPNames_BindViaStaticTable(t *testing.T) {
	dir := bindTableModule(t)
	t.Chdir(dir)

	known := bindTableKnownNodes()

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	channels := map[string]graph.Node{}
	var ids []string
	for _, n := range res.Nodes {
		if n.Type != graph.NodeTypeChannel {
			continue
		}
		channels[n.Label] = n
		ids = append(ids, n.ID)
	}

	want := map[string]struct{ exchange, key, queue string }{
		"build_logs/logs.build.*":      {"build_logs", "logs.build.*", "build_logs_queue"},
		"build_logs/logs.workflow.*":   {"build_logs", "logs.workflow.*", "build_logs_queue"},
		"container_events/container.#": {"container_events", "container.#", "container_events_queue"},
	}
	if len(channels) != len(want) {
		t.Fatalf("got %d channels %v, want %d", len(channels), keysOf(channels), len(want))
	}
	for label, w := range want {
		n, ok := channels[label]
		if !ok {
			t.Fatalf("missing channel %q; got %v", label, keysOf(channels))
		}
		if n.ID != "svc:channel:"+label {
			t.Errorf("channel %q: ID=%q", label, n.ID)
		}
		if n.Meta["exchange"] != w.exchange || n.Meta["routing_key"] != w.key {
			t.Errorf("channel %q: exchange=%q key=%q, want %q/%q", label, n.Meta["exchange"], n.Meta["routing_key"], w.exchange, w.key)
		}
		if n.Meta["queue_name"] != w.queue {
			t.Errorf("channel %q: queue_name=%q, want %q", label, n.Meta["queue_name"], w.queue)
		}
		if n.Meta["resolved_via"] != "static_table" || n.Meta["table_type"] != "QueueDecl" {
			t.Errorf("channel %q: meta=%v, want resolved_via=static_table table_type=QueueDecl", label, n.Meta)
		}
		if n.Meta["pattern"] != "amqp_queue_bind" {
			t.Errorf("channel %q: pattern=%q", label, n.Meta["pattern"])
		}
		// Provenance is the declaring row, not the generic bind loop.
		if n.File != "main.go" || n.Line == 0 {
			t.Errorf("channel %q: File=%q Line=%d, want main.go with the row's line", label, n.File, n.Line)
		}
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("channel nodes are not ID-sorted: %v", ids)
	}

	// Every table channel subscribes into the function that performed the bind.
	var subs int
	for _, e := range res.Edges {
		if e.Meta["via"] != "amqp_static_table" {
			continue
		}
		subs++
		if e.Type != graph.EdgeTypeSubscribes {
			t.Errorf("edge %s: type=%s, want subscribes", e.ID, e.Type)
		}
		if !known[e.To] {
			t.Errorf("subscribes edge to unknown node %q", e.To)
		}
	}
	if subs != len(want) {
		t.Errorf("got %d static-table subscribes edges, want %d", subs, len(want))
	}
}

func keysOf(m map[string]graph.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
