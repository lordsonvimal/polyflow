render json: {
  amqp_progress_events_queue_name: vega_progress_events_queue(org),
  amqp_audit_events_queue_name: vega_audit_events_queue(org),
  amqp_queue_name: generic_task_queue(org)
}

def progress_events_queue_name
  CONFIG[organization_name]&.dig(:amqp_progress_events_queue_name)
end

def task_queue_name
  CONFIG[organization_name]&.dig(:amqp_queue_name)
end
