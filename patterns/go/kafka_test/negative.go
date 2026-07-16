//go:build ignore

package negative

// Wrong package alias — must not match kafka_publish/kafka_subscribe.
func example1() {
	w := kafkaClient.NewWriter(WriterConfig{Topic: "orders"})
	_ = w
}

// No package qualifier — plain NewWriter/NewReader without "kafka." prefix.
func example2() {
	w := NewWriter(WriterConfig{Topic: "orders"})
	r := NewReader(ReaderConfig{Topic: "orders"})
	_, _ = w, r
}

// kafka.NewWriter but no Topic field in the config struct.
func example3() {
	w := kafka.NewWriter(kafka.WriterConfig{
		Brokers: []string{"localhost:9092"},
	})
	_ = w
}
