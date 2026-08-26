# frozen_string_literal: true

# DC.4a: rescue_from registers a controller method by symbol exactly like
# before_action does, but with a different argument shape (an exception class
# ahead of the with: keyword). The block form names no method at all and must
# not be treated as one.
class ErrorsController < ApplicationController
  rescue_from ActiveRecord::RecordNotFound, with: :render_not_found

  rescue_from StandardError do |e|
    render plain: e.message, status: 500
  end

  def render_not_found
    render plain: "not found", status: 404
  end
end
