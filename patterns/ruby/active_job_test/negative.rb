class Presenter < BasePresenter
  def perform(data)
    render(data)
  end
end

worker.perform_async(user.id)
task.run_later
