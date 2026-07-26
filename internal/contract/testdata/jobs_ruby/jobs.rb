# X.2 end-to-end fixture: real ActiveJob + delayed_job sites run through the
# actual parser -> matcher -> contract engine path (bug-class rule #6).

class ReportJob < ApplicationJob
  def perform(id)
    Report.generate(id)
  end
end

ReportJob.perform_later(1)

class User
  def deliver_email
    UserMailer.welcome(self).deliver_now
  end
end

user = User.new
user.delay.deliver_email

class Group
  handle_asynchronously :rebuild

  def rebuild
  end
end

def notify_group(g)
  g.rebuild
end

# Negative: RSpec-wrapped enqueue is test-DSL scope (X.0) — must not mint a
# real publisher/job_enqueue edge.
RSpec.describe "jobs" do
  it "enqueues asynchronously" do
    ReportJob.perform_later(2)
  end
end

# Negative: a chained-call receiver can't be honestly resolved to a class —
# must ledger, not guess.
obj.helper.delay.process

# Negative: a simple receiver with no matching class in the graph — the
# heuristic guess never fabricates an edge, it just finds no match.
thing.delay.unknown_receiver_method
