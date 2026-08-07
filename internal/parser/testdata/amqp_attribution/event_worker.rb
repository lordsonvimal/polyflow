# frozen_string_literal: true

# A Sneakers/kicks consumer: `from_queue` is a class-body DSL call, so NO method
# encloses the comm node. Its only honest anchor is the class itself.
class WorkspaceEventWorker < BaseWorker
  include Sneakers::Worker

  from_queue "workspace_events",
             ack: true,
             threads: 4,
             durable: true

  def work(msg)
    ack!
  end
end
