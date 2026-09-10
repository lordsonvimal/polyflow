class NotAController
  # no receiverless filter calls; these must not match
  config.before_action :x
  obj.skip_before_action :y
  before_action_helper :z
end
