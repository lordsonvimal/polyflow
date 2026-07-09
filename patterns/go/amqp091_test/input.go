//go:build ignore

package main

func publishAndConsume(ch *amqp.Channel) {
	ch.Publish("exchange", "routing.key", false, false, amqp.Publishing{})
	ch.Consume("queue", "consumer", true, false, false, false, nil)
	ch.QueueDeclare("my-queue", true, false, false, false, nil)
}
