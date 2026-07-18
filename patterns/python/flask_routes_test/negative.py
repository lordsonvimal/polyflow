"""No Flask route decorators — only plain and non-HTTP decorators."""
import functools


def login_required(f):
    @functools.wraps(f)
    def wrapper(*args, **kwargs):
        return f(*args, **kwargs)
    return wrapper


@login_required
def protected_view():
    return {}


class Resource:
    def get(self, key):
        return {}

    def route(self, path):
        pass
