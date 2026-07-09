//go:build ignore

package main

func publishAndConsume(ch *amqp.Channel) {
	ch.Publish("exchange", "routing.key", false, false, amqp.Publishing{})
	ch.Consume("queue", "consumer", true, false, false, false, nil)
	ch.QueueDeclare("my-queue", true, false, false, false, nil)
}

func modernAPI(ctx Context, ch *amqp.Channel) {
	ch.ExchangeDeclare("dsw.builds", "topic", true, false, false, false, nil)
	ch.PublishWithContext(ctx, "dsw.builds", "build.start", false, false, amqp.Publishing{})
	ch.ConsumeWithContext(ctx, "build-queue", "agent", true, false, false, false, nil)
}
