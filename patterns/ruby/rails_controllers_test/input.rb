class UsersController < ApplicationController
  before_action :authenticate_user!
  around_action :set_locale
  after_action :log_response, only: [:create]
end
