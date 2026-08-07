# frozen_string_literal: true

# The declaring side: the queue name is handed to the agent in the registration
# response, keyed by a field symbol. The value is per-organization, so nothing
# here is a literal.
class AgentsController
  include QueueNames

  def registration_json(agent)
    {
      amqp_progress_events_queue_name: vega_progress_events_queue(organization),
      amqp_audit_events_queue_name: vega_audit_events_queue(organization),
      amqp_url: amqp_url
    }
  end
end
