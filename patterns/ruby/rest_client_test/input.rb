RestClient::Request.execute(method: :get, url: '/api/users')
RestClient::Request.execute(method: :post, url: '/api/users', payload: body)
RestClient::Resource.get('/api/things')
RestClient.send(method, url, { params: payload, raw_response: true })
