"""FastAPI service: user and item endpoints."""
from fastapi import FastAPI, APIRouter

app = FastAPI()
router = APIRouter(prefix="/items")


@app.get("/users")
async def list_users():
    return []


@app.post("/users")
async def create_user(user: dict):
    return user


@router.get("/{item_id}")
async def get_item(item_id: int):
    return {"id": item_id}


@app.delete("/users/{user_id}")
async def delete_user(user_id: int):
    return {"deleted": user_id}
