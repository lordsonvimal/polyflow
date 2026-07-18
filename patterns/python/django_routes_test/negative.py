"""No Django URL patterns — uses other function calls with different names."""
import os


def configure(settings):
    pass


def path_join(base, rel):
    return os.path.join(base, rel)


# path() here refers to os.path, not django.urls — excluded by package gate.
# At runtime this file would not be a urls.py but the gate is the safeguard.
result = path_join("/base", "segment")
