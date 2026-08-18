package parser

import "os"

// SourceCache holds file contents pre-read by the indexer's hash pre-pass
// (which must read every file's bytes to hash it), keyed by the same path
// each Parser.Parse call receives. Without this, every parsed file was read
// from disk twice per index run: once to hash it, once to parse it.
//
// Passed explicitly through WorkerPool/Parse rather than kept in a package
// var: a package-global cache is only safe under the single-flight
// assumption that one process runs at most one index at a time, which holds
// in production but not when tests run multiple indexer.Run calls
// concurrently in the same binary (t.Parallel()) — that combination raced
// on the global under -race.
type SourceCache map[string][]byte

// readSource returns file's contents, preferring cache and falling back to
// disk on a miss — e.g. files read outside the indexer's main parse phase,
// or in tests that call a Parser directly with a nil cache.
func readSource(file string, cache SourceCache) ([]byte, error) {
	if data, ok := cache[file]; ok {
		return data, nil
	}
	return os.ReadFile(file)
}
