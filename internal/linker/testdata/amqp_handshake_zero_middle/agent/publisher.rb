# frozen_string_literal: true

module Messaging
  class Publisher
    CONFIG = Concurrent::Map.new

    class << self
      def task_queue_name
        CONFIG[organization_name]&.dig(:amqp_queue_name)
      end

      def publish_task_event(message:, durable: true)
        Messaging::Publisher.amqp_session_pool&.with do |channel|
          queue = channel.queue(Messaging::Publisher.task_queue_name, durable: durable)
          queue && publish_message(queue, message)
        end
      end
    end
  end
end
