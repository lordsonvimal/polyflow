class ReportJob < ApplicationJob
  queue_as :default

  def perform(user_id)
    Report.generate(user_id)
  end
end

ReportJob.perform_later(user.id)
ReportJob.set(wait: 5.minutes).perform_later(user.id)
