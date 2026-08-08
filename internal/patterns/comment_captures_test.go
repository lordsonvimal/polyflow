package patterns_test

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/require"
)

// findMatch returns the single match for pattern, failing if there is not
// exactly one.
func findMatch(t *testing.T, results []patterns.MatchResult, pattern string) patterns.MatchResult {
	t.Helper()
	var found []patterns.MatchResult
	for _, r := range results {
		if r.PatternName == pattern {
			found = append(found, r)
		}
	}
	require.Len(t, found, 1, "expected exactly one %s match", pattern)
	return found[0]
}

// A trailing comment on each argument line is a named sibling, so it used to
// bind to an anchored `(_)` capture and shift every later capture by one —
// `routing_key` came back as the literal text "// exchange".
func TestMatch_TrailingArgCommentsDoNotShiftCaptures(t *testing.T) {
	src := []byte(`package p

func publish(routingKey string, body []byte) error {
	return ch.PublishWithContext(
		ctx,
		p.exchange,  // exchange
		routingKey,  // routing key
		false,       // mandatory
		false,       // immediate
		amqp.Publishing{Body: body},
	)
}
`)
	reg := mustLoadRegistry(t, "../../patterns/go/amqp091.yaml")
	m := patterns.NewTreeSitterMatcher(reg)
	results, err := m.Match("go", "publisher.go", src)
	require.NoError(t, err)

	got := findMatch(t, results, "amqp_publish")
	require.Equal(t, "p.exchange", got.Captures["exchange"])
	require.Equal(t, "routingKey", got.Captures["routing_key"])
	require.Equal(t, "false", got.Captures["mandatory"])
	require.Equal(t, "false", got.Captures["immediate"])
}

// The same call without comments must be unaffected by the repair.
func TestMatch_UncommentedArgsUnchanged(t *testing.T) {
	src := []byte(`package p

func publish(routingKey string, body []byte) error {
	return ch.PublishWithContext(ctx, p.exchange, routingKey, false, false, amqp.Publishing{Body: body})
}
`)
	reg := mustLoadRegistry(t, "../../patterns/go/amqp091.yaml")
	m := patterns.NewTreeSitterMatcher(reg)
	results, err := m.Match("go", "publisher.go", src)
	require.NoError(t, err)

	got := findMatch(t, results, "amqp_publish")
	require.Equal(t, "p.exchange", got.Captures["exchange"])
	require.Equal(t, "routingKey", got.Captures["routing_key"])
}

// A leading comment shifts the whole sequence, not just its tail: the exchange
// itself would otherwise be captured as the comment text.
func TestMatch_LeadingArgCommentDoesNotShiftCaptures(t *testing.T) {
	src := []byte(`package p

func publish(routingKey string, body []byte) error {
	return ch.PublishWithContext(
		ctx, // context
		p.exchange,
		routingKey,
		false,
		false,
		amqp.Publishing{Body: body},
	)
}
`)
	reg := mustLoadRegistry(t, "../../patterns/go/amqp091.yaml")
	m := patterns.NewTreeSitterMatcher(reg)
	results, err := m.Match("go", "publisher.go", src)
	require.NoError(t, err)

	got := findMatch(t, results, "amqp_publish")
	require.Equal(t, "p.exchange", got.Captures["exchange"])
	require.Equal(t, "routingKey", got.Captures["routing_key"])
}

// key_dynamic_raw records only the *first* dynamic key field of a node, so a
// publish whose exchange and routing key are both expressions used to pick one
// at random: r.KeyNodes is a map, and ranging it gave a different meta value
// between otherwise-identical indexes. Repeat enough times that map-order
// randomization would show.
func TestMatchToGraph_DynamicKeyRawIsStableAcrossRuns(t *testing.T) {
	src := []byte(`package p

func publish(routingKey string, body []byte) error {
	return ch.PublishWithContext(ctx, p.exchange, routingKey, false, false, amqp.Publishing{Body: body})
}
`)
	reg := mustLoadRegistry(t, "../../patterns/go/amqp091.yaml")
	m := patterns.NewTreeSitterMatcher(reg)

	for i := 0; i < 50; i++ {
		results, err := m.Match("go", "publisher.go", src)
		require.NoError(t, err)
		nodes, _, _ := patterns.MatchToGraph("svc", results)
		require.Len(t, nodes, 1)
		require.Equal(t, "true", nodes[0].Meta["key_dynamic"])
		require.Equal(t, "p.exchange", nodes[0].Meta["key_dynamic_raw"],
			"exchange sorts before routing_key, so it must always win")
	}
}

// When comments pad a call that genuinely has too few arguments there is
// nothing to re-bind to, and the match must be dropped rather than emitted with
// comment text standing in for an exchange name.
func TestMatch_CommentOnlyPaddingDropsMatch(t *testing.T) {
	src := []byte(`package p

func publish() error {
	return ch.PublishWithContext(
		ctx,
		p.exchange, // exchange
		// routing key
	)
}
`)
	reg := mustLoadRegistry(t, "../../patterns/go/amqp091.yaml")
	m := patterns.NewTreeSitterMatcher(reg)
	results, err := m.Match("go", "publisher.go", src)
	require.NoError(t, err)

	for _, r := range results {
		require.NotEqual(t, "amqp_publish", r.PatternName)
	}
}
