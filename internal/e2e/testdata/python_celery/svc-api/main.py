"""API service: dispatches Celery tasks to the worker service."""
from fastapi import FastAPI
from worker_tasks import send_email, process_order, generate_report

app = FastAPI()


@app.post("/users")
async def create_user(user_id: int, email: str):
    send_email.delay(user_id, email)
    return {"user_id": user_id}


@app.post("/orders")
async def create_order(order_id: int, amount: float):
    process_order.apply_async(args=[order_id, amount], countdown=5)
    return {"order_id": order_id}


@app.post("/reports")
async def request_report(report_id: str):
    generate_report.delay(report_id)
    return {"report_id": report_id}
