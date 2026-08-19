# Ordinary keyword pair, not message_type — must not match
# amqp_message_type_pair.
create_thing(status: SOME_CONST)

# An unrelated case/when on a different hash key — must not match
# amqp_message_type_dispatch: the discriminator key isn't "message_type".
def dispatch_by_kind(item)
  case item["kind"]
  when SOME_KIND
    KindHandler.new(item)
  end
end

# A message_type case whose branch does not call a bare Constant.method(...)
# (a local variable receiver, not a class) — must not match.
def handled_locally(message_hash)
  case message_hash["message_type"]
  when MT_NOOP
    local_handler.process(message_hash)
  end
end
