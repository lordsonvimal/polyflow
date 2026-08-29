# frozen_string_literal: true

# ActiveSupport::Concern shape: the callback registration lives inside
# `included do`, not the module body directly (a bare `after_create` there
# would raise at load time). DC.20's gap was that the model including this
# concern was never identified, so the registration was ledgered
# `rails_filter_unattributed` instead of producing an edge.
module BatchChangeNotifier
  extend ActiveSupport::Concern

  included do
    after_create :notify_batch_create
  end

  def notify_batch_create
    BatchChangeMailer.notify(self)
  end
end
