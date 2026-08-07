# frozen_string_literal: true

# The queue-name module lives in its own file, called from both the controller
# that declares the handshake and the worker that consumes the queue. Tier 2 is
# same-file scoped, so this file is exactly what it cannot see from either.
module QueueNames
  def queue_name(organization, name)
    "#{organization.name} #{name}".downcase.parameterize.underscore
  end

  def vega_progress_events_queue(organization)
    queue_name(organization, "vega progress events")
  end

  def vega_audit_events_queue(organization)
    queue_name(organization, "vega audit events")
  end
end
