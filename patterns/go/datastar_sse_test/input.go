//go:build ignore

package main

import "github.com/starfederation/datastar/sdk/go"

func handler(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)
	datastar.MergeFragments(sse, "<div>hello</div>")
	datastar.MergeSignals(sse, map[string]any{"count": 1})
}
