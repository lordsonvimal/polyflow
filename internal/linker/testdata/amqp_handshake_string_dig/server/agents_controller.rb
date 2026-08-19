# frozen_string_literal: true

class AgentsController
  include QueueNames

  def registration_json(agent)
    {
      amqp_queue_name: generic_task_queue(organization)
    }
  end
end
