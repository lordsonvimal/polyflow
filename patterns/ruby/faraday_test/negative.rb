conn = Excon.new(url: "https://api.example.com")
response = conn.fetch("/users")
records.remove(:stale)
