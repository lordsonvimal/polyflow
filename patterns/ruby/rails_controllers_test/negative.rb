module UserHelpers
  def format_name(user)
    user.name.titleize
  end
end

validate :status
