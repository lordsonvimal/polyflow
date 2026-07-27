render json: {
  amqp_progress_events_queue_name: cdr_progress_events_queue(org),
  amqp_audit_events_queue_name: cdr_audit_events_queue(org)
}

def progress_events_queue_name
  CONFIG[organization_name]&.dig(:amqp_progress_events_queue_name)
end
