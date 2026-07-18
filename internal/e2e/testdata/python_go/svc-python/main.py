"""FastAPI service: exposes /items and calls svc-go for user data."""
import requests
from fastapi import FastAPI

app = FastAPI()


@app.get("/items")
async def list_items():
    # Call the Go gin service to fetch associated users.
    resp = requests.get("http://svc-go/users")
    return {"users": resp.json()}


@app.post("/items")
async def create_item(item: dict):
    resp = requests.post("http://svc-go/users", json={"name": item.get("owner")})
    return {"item": item, "user": resp.json()}
