# frozen_string_literal: true

# The consuming side, in the declaring service. Its queue resolves to a literal
# in-file (Tier 2, via the ternary's fallback branch) — this is the node the
# agent's publish site must reach.
class VegaProgressEventWorker
  include Sneakers::Worker

  QUEUE_NAMES = Class.new { include QueueNames }.new

  class << self
    def resolved_queue_name
      org = amqp_organization
      org ? QUEUE_NAMES.vega_progress_events_queue(org) : "vega_progress_events"
    end
  end

  from_queue resolved_queue_name

  def work(message)
    process(message)
  end
end
