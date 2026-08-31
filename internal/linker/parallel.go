package linker

import (
	"runtime"
	"sync"
)

// mapParallel applies fn to every item concurrently (bounded by GOMAXPROCS)
// and returns the results in input order, so the serial merge that follows a
// call produces byte-identical output to the old sequential loop.
//
// fn must be safe for concurrent use. The ruby_* per-file scanners are:
// rubyParse is mutex-guarded, patterns.RelativizeToCwd is pure, the
// receiver-type walkers only read the shared ivarType/methodReturnType maps
// (fully populated before that point), and every fn only writes to its own
// return value.
func mapParallel[E any, T any](items []E, fn func(E) T) []T {
	out := make([]T, len(items))
	if len(items) < 2 {
		for i, it := range items {
			out[i] = fn(it)
		}
		return out
	}
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Add(1)
		go func(i int, it E) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = fn(it)
		}(i, it)
	}
	wg.Wait()
	return out
}

// filterRubyFiles keeps only .rb/.rake paths, preserving order.
func filterRubyFiles(files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if isRubyFile(f) {
			out = append(out, f)
		}
	}
	return out
}
