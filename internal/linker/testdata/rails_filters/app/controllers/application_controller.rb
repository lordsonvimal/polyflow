# frozen_string_literal: true

# A base controller: it declares a filter and defines no actions of its own.
# The class-scope edge is the only one it can produce.
class ApplicationController < ActionController::Base
  include SecurityChecks

  before_action :authenticate_user!

  private

  def authenticate_user!
    redirect_to login_path unless current_user
  end
end
