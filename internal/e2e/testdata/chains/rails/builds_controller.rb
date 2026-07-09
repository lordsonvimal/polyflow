class BuildsController < ApplicationController
  def create
    payload = { sis_id: params[:sis_id], kind: params[:kind] }
    exchange.publish(payload.to_json, routing_key: "build.start")
    render json: { queued: true }
  end

  private

  def exchange
    @exchange ||= channel.topic(ENV.fetch("BUILD_EXCHANGE"))
  end
end
