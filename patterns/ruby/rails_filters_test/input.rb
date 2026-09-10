class WidgetsController < ApplicationController
  before_action :authenticate_user!
  before_action :set_widget, only: %i[show update]
  around_action :with_lock
  after_action :log_access, except: :index
  skip_before_action :authenticate_user!, only: :index
  skip_around_action :with_lock
  skip_after_action :log_access

  before_action do
    ensure_fresh(widget)
  end

  rescue_from ActiveRecord::RecordNotFound, with: :not_found

  def index; end
  def show; end
  def update; end

  private

  def set_widget; end
  def not_found; end
end

class Widget < ApplicationRecord
  before_save :normalize_name
  after_commit :reindex
end
