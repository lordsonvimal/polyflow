class VegaProgressEventWorker < BaseWorker
  include Sneakers::Worker
  from_queue "vega_progress_events", ack: true, threads: 4
end

class AuditWorker < BaseWorker
  from_queue resolved_queue_name, ack: true
end
