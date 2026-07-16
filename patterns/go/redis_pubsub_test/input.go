//go:build ignore

package main

import (
	"context"

	redis "github.com/redis/go-redis/v9"
)

func publishMessage(rdb *redis.Client, ctx context.Context) {
	rdb.Publish(ctx, "notifications", "hello")
}

func subscribeChannel(rdb *redis.Client, ctx context.Context) {
	pubsub := rdb.Subscribe(ctx, "notifications")
	_ = pubsub
}
