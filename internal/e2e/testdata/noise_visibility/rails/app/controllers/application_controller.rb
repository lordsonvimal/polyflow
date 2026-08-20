class ApplicationController < ActionController::Base
  def authenticate_user!
    true
  end

  def verify_organization!
    true
  end
end
