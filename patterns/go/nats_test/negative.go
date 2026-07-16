//go:build ignore

package negative

// 5-argument Publish (AMQP shape) must not match the 2-arg NATS pattern.
func example1(ch Chan) {
	ch.Publish("exchange", "routing.key", false, false, msg)
}

// Different method names entirely.
func example2(nc Conn) {
	nc.Emit("orders.created", data)
	nc.Listen("orders.created", handler)
}

// Publish with 3 args (Redis shape: ctx, channel, message) must not match.
func example3(client Client, ctx Ctx) {
	client.Publish(ctx, "orders.created", payload)
}
