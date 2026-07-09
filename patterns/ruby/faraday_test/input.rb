conn = Faraday.new(url: 'http://api-svc')
conn.get('/api/users')
conn.post('/api/users', payload)
