"""Service module: user management."""
import os
from pathlib import Path


class UserService:
    """Manages user records."""

    def __init__(self, db_path):
        self.db_path = db_path

    def get_user(self, user_id):
        result = load_record(user_id)
        return result

    def create_user(self, name, email):
        validate(name)
        record = build_record(name, email)
        return record


def load_record(user_id):
    return {"id": user_id}


def validate(value):
    if not value:
        raise ValueError("empty")


def build_record(name, email):
    return {"name": name, "email": email}


def top_level_setup():
    path = resolve_path()
    return path


def resolve_path():
    return Path(os.getcwd())
