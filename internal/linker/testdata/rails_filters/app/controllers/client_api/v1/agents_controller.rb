# frozen_string_literal: true

module ClientApi
  module V1
    class AgentsController < SecuredAgentsController
      include TokenAuthenticatable

      before_action :ensure_valid_token
      before_action :load_agent, only: %i[show update]
      after_action :audit, except: [:index]
      around_action :with_timing, if: :slow?

      def index
        render json: Agent.all
      end

      def show
        render json: @agent
      end

      def update
        render json: @agent
      end

      def register
        render json: { ok: true }
      end

      private

      def load_agent
        @agent = Agent.find(params[:id])
      end

      def audit
        AuditLog.record(action_name)
      end

      def with_timing
        yield
      end
    end
  end
end
