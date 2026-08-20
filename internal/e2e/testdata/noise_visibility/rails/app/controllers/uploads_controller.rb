class UploadsController < ApplicationController
  before_action :authenticate_user!
  before_action :verify_organization!

  def complete_multipart
    finalize_upload
  end

  private

  def finalize_upload
    true
  end
end
