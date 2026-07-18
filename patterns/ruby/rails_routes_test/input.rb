Rails.application.routes.draw do
  get '/users', to: 'users#index'
  post '/users', to: 'users#create'
  resources :projects
  resource :profile
  namespace :admin do
  end
  resources :reports do
    member do
      get :archive
    end
    collection do
      get :recent
    end
  end
end
