package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/meta"
)

func TestHandleDocsCLI_ReflectsSetDocs(t *testing.T) {
	meta.SetCLIDocs([]meta.CLICommand{
		{
			Name:  "index",
			Short: "Build the graph",
			Flags: []meta.CLIFlag{{Name: "full", Usage: "full re-index"}},
		},
		{
			Name:  "config",
			Short: "Edit workspace config",
			Subcommands: []meta.CLICommand{
				{Name: "show", Short: "Print config"},
			},
		},
	})
	t.Cleanup(func() { meta.SetCLIDocs(nil) })

	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/docs/cli", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Commands []meta.CLICommand `json:"commands"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Commands) != 2 {
		t.Fatalf("want 2 top-level commands, got %d", len(resp.Commands))
	}
	if resp.Commands[0].Name != "index" || len(resp.Commands[0].Flags) != 1 {
		t.Fatalf("index command not carried through: %+v", resp.Commands[0])
	}
	if resp.Commands[1].Name != "config" || len(resp.Commands[1].Subcommands) != 1 || resp.Commands[1].Subcommands[0].Name != "show" {
		t.Fatalf("nested subcommand not carried through: %+v", resp.Commands[1])
	}
}

func TestHandleDocsCLI_EmptyBeforeServe(t *testing.T) {
	meta.SetCLIDocs(nil)

	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/docs/cli", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Commands []meta.CLICommand `json:"commands"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Commands) != 0 {
		t.Fatalf("want empty commands, got %+v", resp.Commands)
	}
}
