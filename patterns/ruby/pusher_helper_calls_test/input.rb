class NotificationsController
  def pusher(msg, status)
    PusherClient.new(current_user, msg).push(msg, status)
  end
end

class BaseImporter
  def self.non_instance_pusher(msg, status)
    PusherClient.new(nil, msg).push(msg, status)
  end
end

class Foo
  def bar
    pusher(payload, "ok")
    self.pusher(payload, "ok")
    BaseImporter.non_instance_pusher(payload, "ok")
  end
end
