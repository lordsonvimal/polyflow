package main

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
)

// consumeBuilds is the dsw-agent-shaped consumer on the build queue.
func consumeBuilds(ctx context.Context, ch *amqp.Channel) error {
	msgs, err := ch.ConsumeWithContext(ctx, "build-queue", "agent", true, false, false, false, nil)
	if err != nil {
		return err
	}
	for msg := range msgs {
		runBuild(msg.Body)
	}
	return nil
}

func runBuild(body []byte) {
	// docker build …
	_ = body
}
