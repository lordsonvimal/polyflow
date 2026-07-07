require 'net/http'
require 'faraday'

class UsersController < ApplicationController
  def index
    render json: User.all
  end

  def show
    uri = URI("https://api.example.com/users/#{params[:id]}")
    Net::HTTP.get(uri)
  end
end

conn = Faraday.new(url: 'https://api.example.com')
conn.get('/users')
conn.post('/users', { name: 'Alice' })
