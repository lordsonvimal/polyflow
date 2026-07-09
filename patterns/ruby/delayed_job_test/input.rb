user.delay.send_welcome_email

class Newsletter
  handle_asynchronously :deliver_all
end

Delayed::Job.enqueue(ExportJob.new(user.id))
