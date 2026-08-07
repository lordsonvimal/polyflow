# frozen_string_literal: true

# The reading side, in a different repo. The queue name never appears here: it
# is dug out of the config the registration response filled, by field symbol.
module Messaging
  class Publisher
    CONFIG = Concurrent::Map.new

    class << self
      def progress_events_queue_name
        CONFIG[organization_name]&.dig(:amqp_progress_events_queue_name)
      end

      # A field this repo reads but no service declares — an honest miss that
      # must reach the ledger rather than borrow a neighbouring queue.
      def orphan_events_queue_name
        CONFIG[organization_name]&.dig(:amqp_orphan_events_queue_name)
      end

      def publish_progress_event(message:, durable: true)
        Messaging::Publisher.amqp_session_pool&.with do |channel|
          queue = channel.queue(Messaging::Publisher.progress_events_queue_name, durable: durable)
          queue && publish_message(queue, message)
        end
      end

      def publish_orphan_event(message:, durable: true)
        Messaging::Publisher.amqp_session_pool&.with do |channel|
          queue = channel.queue(Messaging::Publisher.orphan_events_queue_name, durable: durable)
          queue && publish_message(queue, message)
        end
      end
    end
  end
end
