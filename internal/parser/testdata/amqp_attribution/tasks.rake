# frozen_string_literal: true

# The trap case: a module that CLOSES long before the queue declaration. The
# queue lives in a rake task block at the bottom, which no class and no method
# contains — nearest-preceding attribution would wrongly hang it off Kicks.
module Kicks
  WORKERS = %w[
    WorkspaceEventWorker
  ].freeze
end

namespace :vega_events do
  task work: :environment do
    conn = Bunny.new(ENV["AMQP_URL"]).tap(&:start)
    ch = conn.create_channel
    ch.queue("preflight_check", durable: true)
    ch.close
  end
end
