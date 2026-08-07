# frozen_string_literal: true

# A Rails controller whose queue declarations sit in a private method, with a
# class-body `before_action` above them. The callback registration is a call
# site, not a scope -- it must not claim the queues declared 15 lines below it.
module ClientApi
  module V1
    class AgentsController < SecuredAgentsController
      include QueueNames

      before_action :ensure_valid_token

      def register
        render json: { registration: registration_json(found_agent) }
      end

      private

      def registration_json(agent)
        {
          amqp_audit_events_queue_name: vega_audit_events_queue(organization),
          amqp_progress_events_queue_name: vega_progress_events_queue(organization),
          amqp_url: amqp_url
        }
      end
    end
  end
end
