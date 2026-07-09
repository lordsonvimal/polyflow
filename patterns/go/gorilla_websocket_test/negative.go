//go:build ignore

package main

// Non-gorilla shapes: version upgrades, IO reads/writes, message queues.
func other(pkg Manager, f File, q Queue) {
	pkg.Upgrade(version)
	f.ReadAt(buf, off)
	q.WriteBatch(items, opts, extra)
	decoder.ReadHeader()
}
