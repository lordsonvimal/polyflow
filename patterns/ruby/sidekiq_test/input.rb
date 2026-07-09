HardWorker.perform_async(1, 2)
HardWorker.perform_in(5.minutes, 1)
class HardWorker
  include Sidekiq::Worker
end
