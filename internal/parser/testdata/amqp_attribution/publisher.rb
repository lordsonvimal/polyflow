# frozen_string_literal: true

# A bunny publisher: the queue declaration sits inside an instance method, so
# the comm node's enclosing scope is that method.
module Messaging
  class Publisher
    def publish_audit_event(message:, durable: true)
      Messaging::Publisher.amqp_session_pool&.with do |channel|
        queue = channel.queue(Messaging::Publisher.audit_events_queue_name, durable: durable)
        queue && publish_message(queue, message)
      end
    end

    def publish_message(queue, message)
      queue.publish(message)
    end
  end
end
