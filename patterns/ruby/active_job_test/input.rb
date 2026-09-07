class ReportJob < ApplicationJob
  queue_as :default

  def perform(user_id)
    Report.generate(user_id)
  end
end

# CJ: a project base class. Its own superclass is ApplicationJob, so it
# matches the predicated pattern too; the unpredicated candidate pattern
# nominates it a second time and the linker dedupes against the seed.
class OrionBaseJob < ApplicationJob
  def perform(*)
    raise NotImplementedError
  end
end

# CJ: one hop further out — invisible to the predicated pattern, nominated
# only by aj_perform_method_candidate.
class CleanupJob < OrionBaseJob
  def perform(id)
    Cleanup.run(id)
  end
end

ReportJob.perform_later(user.id)
ReportJob.set(wait: 5.minutes).perform_later(user.id)
