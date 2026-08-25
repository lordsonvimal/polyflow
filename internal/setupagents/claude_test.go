package setupagents

import "testing"

func matcherHooked(doc map[string]any, matcher, command string) bool {
	hooks, _ := doc["hooks"].(map[string]any)
	postToolUse, _ := hooks["PostToolUse"].([]any)
	for _, g := range postToolUse {
		group, ok := g.(map[string]any)
		if !ok || group["matcher"] != matcher {
			continue
		}
		hookList, _ := group["hooks"].([]any)
		for _, h := range hookList {
			hm, ok := h.(map[string]any)
			if ok && hm["command"] == command {
				return true
			}
		}
	}
	return false
}

func TestMergeClaudeHooks_WiresBashReadGrepAndEdit(t *testing.T) {
	doc := map[string]any{}
	command := "polyflow hook-context-inject"
	if !mergeClaudeHooks(doc, command) {
		t.Fatal("expected added=true on first merge")
	}
	for _, matcher := range []string{"Bash", "Read", "Grep", "Edit"} {
		if !matcherHooked(doc, matcher, command) {
			t.Errorf("matcher %q not wired with command %q", matcher, command)
		}
	}
}

func TestMergeClaudeHooks_IdempotentAcrossAllMatchers(t *testing.T) {
	doc := map[string]any{}
	command := "polyflow hook-context-inject"
	mergeClaudeHooks(doc, command)
	if added := mergeClaudeHooks(doc, command); added {
		t.Fatal("expected added=false on second merge — hooks already wired")
	}
}

func TestClaudeHooksWired_FalseUntilAllMatchersWired(t *testing.T) {
	doc := map[string]any{}
	if claudeHooksWired(doc) {
		t.Fatal("expected false on an empty doc")
	}
	mergeClaudeHooks(doc, "/usr/local/bin/polyflow hook-context-inject")
	if !claudeHooksWired(doc) {
		t.Fatal("expected true once all matchers are wired")
	}
}

func TestClaudeHooksWired_MatchesAcrossDifferentBinPaths(t *testing.T) {
	// Status checks run on a possibly different machine than setup — the
	// exact polyflow binary path baked into the command shouldn't matter.
	doc := map[string]any{}
	mergeClaudeHooks(doc, "/home/other-user/bin/polyflow hook-context-inject")
	if !claudeHooksWired(doc) {
		t.Fatal("expected true regardless of which machine's bin path was used to wire it")
	}
}
