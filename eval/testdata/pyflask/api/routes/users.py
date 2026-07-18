from fastapi import APIRouter, HTTPException

router = APIRouter(prefix="/users", tags=["users"])


@router.post("/")
async def create_user(name: str, email: str):
    return {"id": 1, "name": name, "email": email}


@router.get("/{user_id}")
async def get_user(user_id: int):
    return {"id": user_id, "name": "Alice"}


@router.put("/{user_id}")
async def update_user(user_id: int, name: str):
    return {"id": user_id, "name": name}


@router.get("/")
async def list_users():
    return [{"id": 1, "name": "Alice"}, {"id": 2, "name": "Bob"}]


@router.delete("/{user_id}")
async def delete_user(user_id: int):
    return {"deleted": user_id}
