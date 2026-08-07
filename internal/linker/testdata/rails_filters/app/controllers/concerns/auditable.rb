# frozen_string_literal: true

# A registration inside `included do` belongs to whichever class later includes
# the module, which this pass cannot name. Ledgered, never guessed.
module Auditable
  extend ActiveSupport::Concern

  included do
    before_action :record_audit
  end
end
