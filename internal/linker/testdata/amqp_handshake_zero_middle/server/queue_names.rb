# frozen_string_literal: true

# Minimal, isolated fixture for the Tier AH regression: "amqp_queue_name" has
# no middle segment between "amqp_" and "queue_name" — the shortest legal
# field in the handshake family, and the one the original match regex fell
# one character short of matching. Kept separate from testdata/amqp_handshake
# because that fixture's shared publisher class turned out to hit an
# unrelated pre-existing contract-engine ambiguity once a third publish site
# is added to it — not a Tier AH concern.
module QueueNames
  def queue_name(organization, name)
    "#{organization.name} #{name}".downcase.parameterize.underscore
  end

  def generic_task_queue(organization)
    queue_name(organization, "generic task")
  end
end
