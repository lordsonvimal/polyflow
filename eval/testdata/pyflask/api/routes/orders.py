from fastapi import APIRouter

router = APIRouter(prefix="/orders", tags=["orders"])


@router.post("/")
async def create_order(user_id: int, amount: float):
    return {"id": 100, "user_id": user_id, "amount": amount, "status": "pending"}


@router.get("/{order_id}")
async def get_order(order_id: int):
    return {"id": order_id, "status": "pending"}


@router.put("/{order_id}/status")
async def update_order(order_id: int, status: str):
    return {"id": order_id, "status": status}
