package eval

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinCandidateByFile's store lookup is only reached on a unique match, so the
// selection logic is exercised here without a DB; the unique-match path is
// covered end to end by the lobsters corpus.
func candidates(files ...string) []graph.TargetCandidate {
	out := make([]graph.TargetCandidate, 0, len(files))
	for _, f := range files {
		out = append(out, graph.TargetCandidate{ID: "id:" + f, File: f, Type: "function"})
	}
	return out
}

// TestPinCandidateByFile_RejectsWrongSoleMatch — when the label was
// unambiguous, ResolveTarget returns no candidates. The pin must still be
// honoured, or a case would silently measure a declaration it did not name.
func TestPinCandidateByFile_RejectsWrongSoleMatch(t *testing.T) {
	root := &graph.Node{ID: "n1", File: "/repo/app/controllers/cabinet_controller.rb"}

	_, err := pinCandidateByFile(t.Context(), nil, root, nil, "app/controllers/search_controller.rb")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cabinet_controller.rb")
}

func TestPinCandidateByFile_AcceptsCorrectSoleMatch(t *testing.T) {
	root := &graph.Node{ID: "n1", File: "/repo/app/controllers/search_controller.rb"}

	got, err := pinCandidateByFile(t.Context(), nil, root, nil, "app/controllers/search_controller.rb")

	require.NoError(t, err)
	assert.Equal(t, root, got)
}

// TestPinCandidateByFile_NoMatchIsAnError — a corpus case is a fixed reference
// point. If the declaration it names has moved or gone, that must fail loudly
// rather than fall back to an arbitrary same-named node, which is the failure
// this whole mechanism exists to end.
func TestPinCandidateByFile_NoMatchIsAnError(t *testing.T) {
	cands := candidates(
		"/repo/app/controllers/cabinet_controller.rb",
		"/repo/app/controllers/home_controller.rb",
	)

	_, err := pinCandidateByFile(t.Context(), nil, &graph.Node{ID: "n1"}, cands, "app/controllers/search_controller.rb")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no candidate")
}

// TestPinCandidateByFile_AmbiguousSuffixIsAnError — a suffix that still matches
// two declarations has not pinned anything.
func TestPinCandidateByFile_AmbiguousSuffixIsAnError(t *testing.T) {
	cands := candidates(
		"/repo/app/controllers/admin/users_controller.rb",
		"/repo/app/controllers/api/admin/users_controller.rb",
	)

	_, err := pinCandidateByFile(t.Context(), nil, &graph.Node{ID: "n1"}, cands, "admin/users_controller.rb")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "more specific")
}
