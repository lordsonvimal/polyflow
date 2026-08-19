# frozen_string_literal: true

# Minimal, isolated fixture for the string-keyed two-arg dig follow-up: the
# nextGen-CDR-Agent shape where a handshake field is read out of a nested
# response hash by an outer string key rather than by its own symbol, so
# `dig` takes two positional string arguments instead of one symbol. Kept
# separate from testdata/amqp_handshake for the same reason
# amqp_handshake_zero_middle is: a third publish site in that fixture's
# shared publisher class trips an unrelated contract-engine ambiguity.
module QueueNames
  def queue_name(organization, name)
    "#{organization.name} #{name}".downcase.parameterize.underscore
  end

  def generic_task_queue(organization)
    queue_name(organization, "generic task")
  end
end
