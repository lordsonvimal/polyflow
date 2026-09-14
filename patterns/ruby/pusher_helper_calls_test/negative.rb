class Something
  def pusher
    @cached_pusher ||= compute
  end
end

def other
  pusher(:only)
  helper.pusher(1, 2)
end
