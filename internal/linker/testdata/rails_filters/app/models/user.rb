# frozen_string_literal: true

# Mirrors orion-atlas's app/models/user.rb: `validate`/`before_validation`
# register a private method exactly the way `before_action` does on a
# controller, and used to be invisible for the same reason -- app/models never
# entered LinkRailsFilters.
class User < ApplicationRecord
  before_validation :set_username
  validate :cro_user_must_be_sso
  before_validation :normalize_email

  # A model has no actions: only:/except: scope a filter to some of a
  # controller's dispatchable methods, which means nothing for an AR instance
  # method. `full_name` is public and unrelated to any callback, and must not
  # pick up a `calls` edge to a callback target the way a controller action
  # would.
  def full_name
    "#{first_name} #{last_name}"
  end

  private

  def set_username
    self.username ||= derive_username
  end

  def cro_user_must_be_sso
    errors.add(:base, "must use SSO") if cro? && !sso_user
  end

  def normalize_email
    self.email = email.to_s.downcase
  end

  def derive_username
    email.to_s.split("@").first
  end
end
