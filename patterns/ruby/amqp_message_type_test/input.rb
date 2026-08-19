def create_user
  create_complex_message(
    message_type: MT_CREATE_USER,
    params: params
  )
end

def fetch_handler_by_message_type(message_hash)
  case message_hash["message_type"]
  when MT_CREATE_USER, MT_UPDATE_USER, MT_CHANGE_ROLE
    UserMessageHandler.new(message_hash)
  when MT_DELETE_STUDY
    StudyMessageHandler.new(message_hash)
  end
end
