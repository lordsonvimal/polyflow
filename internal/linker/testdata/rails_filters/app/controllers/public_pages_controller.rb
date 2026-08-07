# frozen_string_literal: true

# A partial retraction: the landing page must stay reachable without a session,
# every other action still inherits the check.
class PublicPagesController < ApplicationController
  skip_before_action :authenticate_user!, only: %i[landing]

  def landing
    render :landing
  end

  def dashboard
    render :dashboard
  end
end
