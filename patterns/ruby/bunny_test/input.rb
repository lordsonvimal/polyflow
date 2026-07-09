queue.publish('message payload')
queue.subscribe { |msg| handle(msg) }
ch.queue('my-queue')
