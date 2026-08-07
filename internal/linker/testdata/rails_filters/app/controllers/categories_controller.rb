# frozen_string_literal: true

# The inline form. orion writes more than a third of its registrations this
# way, and the lambda body still names a method on the controller.
class CategoriesController < ApplicationController
  before_action :set_study
  before_action -> { require_study_access(@study) }
  before_action -> { can_manage_task_for_study?(@study) }
  before_action(only: %i[index]) { ensure_study }
  before_action { Rails.logger.info("noise") }

  def index
    render json: @study.categories
  end

  def show
    render json: @study
  end

  private

  def set_study
    @study = Study.find(params[:study_id])
  end

  def require_study_access(study)
    head :forbidden unless study.accessible_by?(current_user)
  end

  def ensure_study
    head :not_found if @study.nil?
  end
end
