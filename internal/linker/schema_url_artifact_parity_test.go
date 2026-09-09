package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/artifact"
)

// The endpoint_table gate's normalizer chain must stay byte-identical to
// NormalizeSchemaPath — link_passes builds handlerPaths with the latter and the
// gate corroborates against it, so any drift silently drops corroboration.
// SA.3 acceptance: zero change to cedar's endpoint table across the refactor.
func TestEndpointTableNormalizerChainMatchesNormalizeSchemaPath(t *testing.T) {
	t.Parallel()
	m, ok := artifact.MappingFor("endpoint_table")
	if !ok {
		t.Fatal("endpoint_table mapping missing")
	}
	g := m.GateSpec()

	cases := []string{
		"/api/gadgets/{gadget_id}/widgets",
		"/api/widgets/<id>",
		"/api/gadgets/:id/reorder",
		"/api/things/${id}?type=Form&x=1",
		"/api//double///slash/",
		"/api/standards/${standard_id}/forms/${id}",
		"relative/path",
		"https://host/x",
		"",
		"/",
		"#frag-only",
		"/trailing/",
		"/a/b#x?y",
	}
	for _, in := range cases {
		want, wantOK := NormalizeSchemaPath(in)
		got, gotOK := g.Norm(in)
		if gotOK != wantOK || got != want {
			t.Errorf("%q: gate.Norm = (%q,%v), NormalizeSchemaPath = (%q,%v)", in, got, gotOK, want, wantOK)
		}
	}
}
