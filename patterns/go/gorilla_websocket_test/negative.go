//go:build ignore

package main

// Non-gorilla shapes: version upgrades, IO reads/writes, message queues.
func other(pkg Manager, f File, q Queue) {
	pkg.Upgrade(version)
	f.ReadAt(buf, off)
	q.WriteBatch(items, opts, extra)
	decoder.ReadHeader()
}

// A string switch over a non-message field and an untyped encode must not
// match the typed-dispatch/typed-send patterns.
func shipping(order Order, enc Encoder) {
	switch order.Status {
	case "shipped":
		notify(order)
	}
	enc.Encode(Ack{Status: "ok"})
}
