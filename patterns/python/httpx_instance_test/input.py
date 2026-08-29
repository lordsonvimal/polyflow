"""Service that calls a remote API through an httpx.Client instance."""
import httpx

c = httpx.Client(base_url="http://api-svc")


def fetch_users():
    resp = c.get("/users")
    return resp.json()


def create_user(data):
    resp = c.post("/users", json=data)
    return resp.json()
