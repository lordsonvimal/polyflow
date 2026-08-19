# Wrong receiver: not RestClient at all.
def other_request(method, payload, headers: {})
  OtherClient::Request.execute(
    method: method,
    url: payload[:url]
  )
end

# url: value does not come from the wrapper's own parameter — it's a
# hardcoded literal, so there is no forwarding relationship to record.
def literal_request(method, payload, headers: {})
  RestClient::Request.execute(
    method: method,
    url: "https://example.com/fixed"
  )
end

# Hash-index key is not :url — a different field, must not match.
def token_request(method, payload, headers: {})
  RestClient::Request.execute(
    method: method,
    url: payload[:endpoint]
  )
end
