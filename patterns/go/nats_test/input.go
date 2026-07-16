//go:build ignore

package main

import nats "github.com/nats-io/nats.go"

func publishAndSubscribe(nc *nats.Conn) {
	nc.Publish("orders.created", []byte("payload"))
	nc.Subscribe("orders.created", func(m *nats.Msg) {
		// handle message
	})
}
