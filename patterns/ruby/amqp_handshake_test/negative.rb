render json: { user_id: 1, amqp_url: ENV["AMQP_URL"], status: :ok }
config.dig(:database_url)
