"""No FastAPI route decorators — only plain functions and non-HTTP decorators."""


def get_data(key: str):
    return {}


def post_update(item):
    return item


class DataManager:
    def get(self, key):
        return self.store.get(key)

    def post(self, key, value):
        self.store[key] = value


@staticmethod
def utility():
    pass


@property
def name(self):
    return self._name
