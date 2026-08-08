Rails.application.routes.draw do
  post "login", to: "sessions#create"

  namespace :client_api do
    namespace :v1 do
      get "user_category_rules/:id", to: "user_category_rules#show"
    end
  end
end
