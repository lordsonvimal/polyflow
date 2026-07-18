"""No requests library calls — only dict/object method calls that superficially
resemble HTTP verbs but are not on the 'requests' identifier."""


class Cache:
    def get(self, key):
        return self.data.get(key)

    def post(self, key, value):
        self.data[key] = value

    def delete(self, key):
        del self.data[key]


cache = Cache()
result = cache.get("user:1")
config = {"key": "val"}
value = config.get("key")
