queue.publish('message payload')
queue.subscribe { |msg| handle(msg) }
ch.queue('my-queue')
exchange.publish(payload.to_json, routing_key: "build.start")
