def rest_request(method, payload, headers: {})
  RestClient::Request.execute(
    headers: { content_type: :json }.merge(headers),
    method: method,
    payload: payload.to_json,
    raw_response: true,
    url: payload[:url]
  )
end
