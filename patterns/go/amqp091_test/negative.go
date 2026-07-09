//go:build ignore

package negative

// Superficially similar pub/sub shapes with wrong arity — must not match
// the 5-arg amqp091 Publish / Consume / QueueDeclare signatures.
func run(p Producer, ch Chan) {
	p.Publish("topic", msg)
	ch.Consume("queue")
	ch.QueueDeclare("jobs")
	bus.Emit("user.created", payload)
}
func modernNegatives(ctx Context, p Producer) {
	p.PublishWithContext(ctx, msg)          // wrong arity
	p.ConsumeWithContext(ctx)               // wrong arity
	registry.ExchangeDeclare(name)          // wrong arity
}
