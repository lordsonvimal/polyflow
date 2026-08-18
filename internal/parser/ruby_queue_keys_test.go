package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestParameterizeQueueName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"cdr progress events":  "cdr_progress_events",
		"cdr_progress_events":  "cdr_progress_events",
		"  Audit Events  ":     "audit_events",
		"sce-lro-events":       "sce_lro_events",
		"Workspace  Compare!!": "workspace_compare",
		"":                     "",
		"***":                  "",
	}
	for in, want := range cases {
		assert.Equal(t, want, parameterizeQueueName(in), "parameterize(%q)", in)
	}
}

func TestRubyQueueMethodRef(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"resolved_queue_name":                             "resolved_queue_name",
		"QUEUE_NAMES.cdr_progress_events_queue(org)":      "cdr_progress_events_queue",
		"Messaging::Publisher.progress_events_queue_name": "progress_events_queue_name",
		`CONFIG[org].dig(:amqp_x)`:                        "", // not a bare method ref
		`"literal"`:                                       "",
		"a.b.c":                                           "c",
	}
	for in, want := range cases {
		assert.Equal(t, want, rubyQueueMethodRef(in), "methodRef(%q)", in)
	}
}

// Mirrors the real MainSvc worker shape: a `from_queue resolved_queue_name`
// consumer whose queue name resolves to the literal in the same-file ternary
// fallback via QueueNames.
func TestResolveRubyQueueKeys_WorkerFallback(t *testing.T) {
	t.Parallel()
	src := []byte(`
class CdrProgressEventWorker < BaseWorker
  include Sneakers::Worker

  class << self
    def resolved_queue_name
      org = amqp_organization
      org ? QUEUE_NAMES.cdr_progress_events_queue(org) : "cdr_progress_events"
    end
  end

  from_queue resolved_queue_name, ack: true, threads: 4
end
`)
	// The pattern matcher would have produced this channel node (key_dynamic
	// because resolved_queue_name is a method reference, not a literal).
	nodes := []graph.Node{
		{
			ID:      "main-svc:channel:x",
			Type:    graph.NodeTypeChannel,
			Service: "main-svc",
			Label:   "dynamic",
			Meta: map[string]string{
				"key_dynamic":     "true",
				"key_dynamic_raw": "resolved_queue_name",
			},
		},
	}
	resolveRubyQueueKeys("cdr_progress_event_worker.rb", src, nodes)

	assert.Equal(t, "cdr_progress_events", nodes[0].Meta["queue_name"],
		"resolved_queue_name must resolve to its same-file literal fallback")
	assert.Empty(t, nodes[0].Meta["key_dynamic"], "key_dynamic must be cleared once resolved")
	assert.Equal(t, "ruby_queue_method", nodes[0].Meta["key_resolved_via"])
}

// The queue_name(org, "human name") builder maps its enclosing method to the
// parameterized literal even though the method name itself contains it.
func TestResolveRubyQueueKeys_BuilderCall(t *testing.T) {
	t.Parallel()
	src := []byte(`
module QueueNames
  def cdr_progress_events_queue(organization)
    queue_name(organization, "cdr progress events")
  end
end
`)
	nodes := []graph.Node{
		{
			ID:      "consumer-a:channel:y",
			Type:    graph.NodeTypeChannel,
			Service: "consumer-a",
			Meta: map[string]string{
				"key_dynamic":     "true",
				"key_dynamic_raw": "cdr_progress_events_queue(org)",
			},
		},
	}
	resolveRubyQueueKeys("queue_names.rb", src, nodes)
	assert.Equal(t, "cdr_progress_events", nodes[0].Meta["queue_name"])
}

// A genuinely runtime-only reference (config lookup, no literal anywhere in the
// file) must stay key_dynamic — never guessed.
func TestResolveRubyQueueKeys_RuntimeOnly_StaysDynamic(t *testing.T) {
	t.Parallel()
	src := []byte(`
module Messaging
  class Publisher
    def progress_events_queue_name
      CONFIG[organization_name]&.dig(:amqp_progress_events_queue_name)
    end
  end
end
`)
	nodes := []graph.Node{
		{
			ID:      "consumer-a:channel:z",
			Type:    graph.NodeTypeChannel,
			Service: "consumer-a",
			Meta: map[string]string{
				"key_dynamic":     "true",
				"key_dynamic_raw": "Messaging::Publisher.progress_events_queue_name",
			},
		},
	}
	resolveRubyQueueKeys("publisher.rb", src, nodes)
	assert.Equal(t, "true", nodes[0].Meta["key_dynamic"],
		"a runtime-only queue name must remain an honest key_dynamic miss")
	assert.Empty(t, nodes[0].Meta["queue_name"])
}
