# frozen_string_literal: true

class ReportsController < ApplicationController
  skip_before_action :authenticate_user!
  before_action :nonexistent_filter

  def index
    render json: []
  end
end
