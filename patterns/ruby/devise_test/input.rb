class User < ApplicationRecord
  devise :database_authenticatable, :registerable, :recoverable

  private

  def password_required?
    super
  end
end
