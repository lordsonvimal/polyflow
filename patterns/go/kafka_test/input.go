//go:build ignore

package main

import "github.com/segmentio/kafka-go"

func produce() {
	w := kafka.NewWriter(kafka.WriterConfig{
		Brokers: []string{"localhost:9092"},
		Topic:   "orders",
	})
	_ = w
}

func consume() {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{"localhost:9092"},
		Topic:   "orders",
		GroupID: "order-group",
	})
	_ = r
}
