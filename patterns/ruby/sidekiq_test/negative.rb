worker.perform(args)

class Report
  include Comparable
end

job.run_later(5.minutes)
