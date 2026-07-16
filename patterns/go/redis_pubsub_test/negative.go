//go:build ignore

package negative

// 5-arg Publish (AMQP shape) must not match the 3-arg Redis pattern.
func example1(ch Chan) {
	ch.Publish("exchange", "key", false, false, msg)
}

// 2-arg Publish (NATS shape) must not match the 3-arg Redis pattern.
func example2(nc Conn) {
	nc.Publish("subject", data)
}

// Different method names.
func example3(client Client) {
	client.Send(ctx, "channel", msg)
	client.Listen(ctx, "channel")
}
