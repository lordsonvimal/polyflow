# Not a Faraday.new call
conn = HTTPClient.new('http://example.com')
conn = Net::HTTP.new('example.com', 80)

# Faraday.new with no arguments
conn = Faraday.new

# Not an assignment (inline usage)
response = Faraday.new('http://example.com').get('/users')
