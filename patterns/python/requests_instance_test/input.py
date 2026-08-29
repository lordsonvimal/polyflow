"""Service that calls a remote API through a requests.Session instance."""
import requests

s = requests.Session()


def fetch_users():
    resp = s.get("http://api-svc/users")
    return resp.json()


def create_user(data):
    resp = s.post("http://api-svc/users", json=data)
    return resp.json()
