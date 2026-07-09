mount Api => "/api"
scope "/admin" do
  root to: "home#index"
end
config.read("settings.yml")
