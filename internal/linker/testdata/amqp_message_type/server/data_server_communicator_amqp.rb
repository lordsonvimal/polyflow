# frozen_string_literal: true

# The declaring side: message_type is set to a constant defined in this
# repo's own messaging constants file (not shown here — Tier 2 territory).
# Nothing here proves what the CONSUMER does with it; that's this fixture's
# whole point.
class DataServerCommunicatorAmqp
  def create_user(params)
    create_complex_message(
      message_type: MT_CREATE_USER,
      params: params
    )
  end

  def delete_study(params)
    create_complex_message(
      message_type: MT_DELETE_STUDY,
      params: params
    )
  end
end
