"""FastAPI application entry point. Registers routers and dispatches Celery tasks."""
from fastapi import FastAPI
from celery import Celery
from routes import users, orders
from worker_tasks import (
    send_notification,
    process_payment,
    generate_invoice,
    send_verification_email,
    cleanup_expired_sessions,
)

app = FastAPI(title="pyflask API")
app.include_router(users.router)
app.include_router(orders.router)

celery_app = Celery("api", broker="redis://localhost:6379/0")


@app.post("/notify/{user_id}")
async def notify_user(user_id: int, message: str):
    send_notification.delay(user_id, message)
    return {"queued": True}


@app.post("/payments/{order_id}")
async def pay_order(order_id: int, amount: float):
    process_payment.delay(order_id, amount)
    return {"queued": True}


@app.post("/invoices/{order_id}")
async def invoice_order(order_id: int):
    generate_invoice.apply_async(args=[order_id])
    return {"queued": True}


@app.post("/verify/{user_id}")
async def verify_user(user_id: int, email: str):
    send_verification_email.delay(user_id, email)
    return {"queued": True}


@app.post("/cleanup")
async def trigger_cleanup():
    cleanup_expired_sessions.delay()
    return {"queued": True}
