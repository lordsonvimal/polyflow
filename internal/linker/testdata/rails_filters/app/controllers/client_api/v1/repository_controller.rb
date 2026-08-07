# frozen_string_literal: true

# Shares a simple name with the top-level RepositoryController and sits in a
# different hierarchy. Sorted by path it comes first, which is what made the
# simple-name walk pick it.
module ClientApi
  module V1
    class RepositoryController < ClientApi::V1::ApiBaseController
    end
  end
end
