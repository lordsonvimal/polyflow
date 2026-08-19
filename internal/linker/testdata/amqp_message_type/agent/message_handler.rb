# frozen_string_literal: true

# The reading side, in a different repo. Dispatches on the SAME constant name
# the producer used, defined independently in this repo's own
# messaging-constants file.
class MessageHandler
  def fetch_handler_by_message_type(message_hash)
    case message_hash["message_type"]
    when MT_CREATE_USER, MT_UPDATE_USER
      UserMessageHandler.new(message_hash)
    when MT_ORPHAN_TYPE
      OrphanMessageHandler.new(message_hash)
    end
  end
end
