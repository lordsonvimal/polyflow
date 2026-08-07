# frozen_string_literal: true

# The callback lives here, not in the controller that registers it -- the
# ordinary Rails shape, and the reason resolution has to walk ancestors.
module TokenAuthenticatable
  def ensure_valid_token
    head :unauthorized unless valid_token?
  end
end
