"""Service that calls a remote API using the requests library."""
import requests


def fetch_users():
    resp = requests.get("http://api-svc/users")
    return resp.json()


def create_user(data):
    resp = requests.post("http://api-svc/users", json=data)
    return resp.json()


def update_user(user_id, data):
    resp = requests.put(f"http://api-svc/users/{user_id}", json=data)
    return resp.json()


def delete_user(user_id):
    resp = requests.delete(f"http://api-svc/users/{user_id}")
    return resp.ok


def custom_call():
    resp = requests.request("GET", "http://api-svc/health")
    return resp.json()
