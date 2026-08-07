# frozen_string_literal: true

module ClientApi
  module V1
    class ApiBaseController < ActionController::API
      before_action :restrict_access

      private

      def restrict_access
        head :unauthorized unless request.headers["X-Token"]
      end
    end
  end
end
