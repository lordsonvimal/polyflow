# frozen_string_literal: true

# DocumentsController < RepositoryController < ApplicationController, so it is
# authenticated. Resolving `RepositoryController` by simple name walks into
# ClientApi::V1 instead and loses that -- silently, with an api-only chain in
# its place.
class DocumentsController < RepositoryController
  def index
    render json: []
  end
end
