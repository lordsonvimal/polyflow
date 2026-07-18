"""Service that calls a remote API using the httpx library."""
import httpx


def fetch_users():
    resp = httpx.get("http://api-svc/users")
    return resp.json()


def create_user(data):
    resp = httpx.post("http://api-svc/users", json=data)
    return resp.json()


def delete_user(user_id):
    resp = httpx.delete(f"http://api-svc/users/{user_id}")
    return resp.is_success


def custom_call(method, url):
    resp = httpx.request(method, "http://api-svc/health")
    return resp.json()
