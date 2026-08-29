# frozen_string_literal: true

# Mirrors orion-atlas's SceBatch, which `include`s BatchChangeNotifier's
# concern-registered after_create callback.
class Batch < ApplicationRecord
  include BatchChangeNotifier
end
