# CJ: a `perform` method on a class with no superclass at all is not even
# nominated — aj_perform_method_candidate requires a superclass node, so a
# plain PORO never reaches the linker. (A PORO that *does* subclass something,
# like `class Presenter < BasePresenter`, is nominated and then dropped by
# PromoteInheritedJobPerform because it reaches no ActiveJob root; that half is
# covered in internal/linker/ruby_job_inherit_test.go, not here.)
class Presenter
  def perform(data)
    render(data)
  end
end

worker.perform_async(user.id)
task.run_later
