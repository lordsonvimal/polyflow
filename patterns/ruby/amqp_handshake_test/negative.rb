render json: { user_id: 1, amqp_url: ENV["AMQP_URL"], status: :ok }
config.dig(:database_url)

# Adversarial: "queue_name" appears at the end but with no "_" separator
# before it (the middle segment ends in "e", not "_") — must not match the
# widened zero-middle-segment regex either.
render json: { amqp_dequeue_name: some_value }
config.dig(:amqp_dequeue_name)
