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

func TestMergeClaudeHooks_WiresBashReadAndGrep(t *testing.T) {
	doc := map[string]any{}
	command := "polyflow hook-context-inject"
	if !mergeClaudeHooks(doc, command) {
		t.Fatal("expected added=true on first merge")
	}
	for _, matcher := range []string{"Bash", "Read", "Grep"} {
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
