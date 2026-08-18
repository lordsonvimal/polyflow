package parser

import "os"

// sourceCache holds file contents pre-read by the indexer's hash pre-pass
// (which must read every file's bytes to hash it), keyed by the same path
// each Parser.Parse call receives. Without this, every parsed file was read
// from disk twice per index run: once to hash it, once to parse it.
//
// Scoped to the indexer's per-service parse step (set immediately before a
// WorkerPool.Run call, cleared immediately after) rather than kept for the
// life of the process: the jobs manager runs at most one index at a time
// per process (single-flight), so this never races with itself, but a
// process-lifetime cache would risk serving stale bytes for a file that
// changed between two index runs.
var sourceCache map[string][]byte

// SetSourceCache installs (or clears, with nil) the pre-read file cache.
func SetSourceCache(cache map[string][]byte) {
	sourceCache = cache
}

// readSource returns file's contents, preferring the pre-read cache and
// falling back to disk on a miss — e.g. files read outside the indexer's
// main parse phase, or in tests that call a Parser directly.
func readSource(file string) ([]byte, error) {
	if data, ok := sourceCache[file]; ok {
		return data, nil
	}
	return os.ReadFile(file)
}
