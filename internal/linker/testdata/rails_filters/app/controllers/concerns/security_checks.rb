# frozen_string_literal: true

# A module that includes another module. orion's real chain to
# can_manage_task_for_study? runs through exactly this hop, and skipping module
# bodies broke it in six controllers.
module SecurityChecks
  include TaskSecurityChecks
end
