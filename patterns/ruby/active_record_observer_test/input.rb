class UserFileObserver < ActiveRecord::Observer
  observe :user_file

  def after_save(user_file)
    user_file.reload
  end
end
