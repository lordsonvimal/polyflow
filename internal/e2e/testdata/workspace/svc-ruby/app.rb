require 'faraday'

class UsersController
  def create
    conn = Faraday.new(url: 'http://api-svc')
    response = conn.post('/api/users', { name: params[:name] }.to_json)
    render json: response.body
  end

  def show
    conn = Faraday.new(url: 'http://api-svc')
    response = conn.get("/api/users/#{params[:id]}")
    render json: response.body
  end
end
