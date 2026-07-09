package main

import amqp "github.com/rabbitmq/amqp091-go"

func publishUserCreated(ch *amqp.Channel, userID int) {
	ch.Publish("user.events", "user.created", false, false, amqp.Publishing{})
}

func consumeUserCreated(ch *amqp.Channel) {
	ch.Consume("user_notifications", "consumer", true, false, false, false, nil)
}
