package parser_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseRubyController writes src to a controller-shaped path and returns the
// nav_link producer nodes keyed by the destination they named — helper name if
// the target was a route helper, literal path otherwise.
func parseRubyController(t *testing.T, src string) map[string]graph.Node {
	t.Helper()
	m := mustMatcher(t)
	file := filepath.Join(t.TempDir(), "things_controller.rb")
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))

	p := parser.ForFile(file)
	require.NotNil(t, p)
	nodes, _, _, err := p.Parse(file, "svc", m)
	require.NoError(t, err)

	out := map[string]graph.Node{}
	for _, n := range nodes {
		if n.Type != graph.NodeTypeHTTPClient {
			continue
		}
		key := n.Meta["helper"]
		if key == "" {
			key = n.Meta["path"]
		}
		out[key] = n
	}
	return out
}

// TestRedirectNav_HelperShapes covers the dominant form on the fleet:
// `redirect_to <route helper>`, with and without arguments, bare or followed by
// a flash. The flash value is the trap — `notice: t("x")` puts another
// receiverless call in the same call expression, but inside a `pair`, so a
// direct-child match on argument_list cannot mistake it for the destination.
func TestRedirectNav_HelperShapes(t *testing.T) {
	got := parseRubyController(t, `
class ThingsController < ApplicationController
  def create
    redirect_to folder_path(@folder), notice: t("folders.created")
  end

  def destroy
    redirect_to dashboard_path
  end

  def update
    redirect_to study_deliverables_url(@study), alert: flash_message
  end
end
`)

	for _, helper := range []string{"folder_path", "dashboard_path", "study_deliverables_url"} {
		n, ok := got[helper]
		assert.True(t, ok, "no nav producer for %s; got %v", helper, keysOf(got))
		if ok {
			assert.Equal(t, "nav_link_rails_redirect_helper", n.Meta["pattern"])
			assert.Equal(t, "true", n.Meta["nav_link"])
			// A 302 is followed with GET regardless of the verb that produced it.
			assert.Equal(t, "GET", n.Meta["method"])
			// No path yet: the linker resolves the helper against the route
			// table. The marker keeps it out of the same-line nav dedup.
			assert.Equal(t, "true", n.Meta["key_dynamic"])
		}
	}
	assert.NotContains(t, got, "t", "flash i18n call captured as a destination")
	assert.NotContains(t, got, "flash_message")
}

// TestRedirectNav_Literal. `redirect_to "/login"` needs no route table.
func TestRedirectNav_Literal(t *testing.T) {
	got := parseRubyController(t, `
class SessionsController < ApplicationController
  def new
    redirect_to "/saml/login"
  end
end
`)
	n, ok := got[`"/saml/login"`]
	if !ok {
		n, ok = got["/saml/login"]
	}
	require.True(t, ok, "no nav producer for the literal redirect; got %v", keysOf(got))
	assert.Equal(t, "nav_link_rails_redirect_literal", n.Meta["pattern"])
	assert.Equal(t, "true", n.Meta["nav_link"])
}

// TestRedirectNav_UnresolvableTargetsAreNotDestinations. Roughly a third of the
// fleet's redirects name something that is not a route helper at all. Each of
// these must produce no nav producer rather than a phantom http_client naming a
// local variable — the same artifact the C.2 helper gate removed for link_to.
func TestRedirectNav_UnresolvableTargetsAreNotDestinations(t *testing.T) {
	got := parseRubyController(t, `
class ThingsController < ApplicationController
  def a
    redirect_to request.referer
  end

  def b
    redirect_to @folder.uri
  end

  def c
    redirect_to :back
  end

  def d
    redirect_to trash_folder
  end

  def e
    redirect_to url
  end

  def f
    redirect_to logout_response
  end
end
`)
	assert.Empty(t, got, "expected no nav producers; got %v", keysOf(got))
}

// TestRedirectNav_RouteShapedButUnknownIsKept. `redirect_path` is a local in the
// fleet, but the suffix cannot prove that, and an unresolved entry is an honest
// gap while a dropped one is a lie about coverage. Same call as C.2's.
func TestRedirectNav_RouteShapedButUnknownIsKept(t *testing.T) {
	got := parseRubyController(t, `
class ThingsController < ApplicationController
  def show
    redirect_to redirect_path
  end
end
`)
	assert.Contains(t, got, "redirect_path")
}

func keysOf(m map[string]graph.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
