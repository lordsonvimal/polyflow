# frozen_string_literal: true

class GenericTaskWorker
  include Sneakers::Worker

  QUEUE_NAMES = Class.new { include QueueNames }.new

  class << self
    def resolved_queue_name
      org = amqp_organization
      org ? QUEUE_NAMES.generic_task_queue(org) : "generic_task"
    end
  end

  from_queue resolved_queue_name

  def work(message)
    process(message)
  end
end
