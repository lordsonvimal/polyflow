"""Celery worker task definitions for the pyflask application."""
from celery import Celery, shared_task

app = Celery("worker", broker="redis://localhost:6379/0")


@app.task
def send_notification(user_id: int, message: str):
    """Send an in-app or push notification to a user."""
    pass


@app.task(bind=True, max_retries=3)
def process_payment(self, order_id: int, amount: float):
    """Process a payment with automatic retry on transient failures."""
    pass


@shared_task
def generate_invoice(order_id: int):
    """Generate a PDF invoice for a completed order."""
    pass


@shared_task(bind=True)
def send_verification_email(self, user_id: int, email: str):
    """Send an email verification link to a newly registered user."""
    pass


@app.task
def cleanup_expired_sessions():
    """Purge expired session tokens from the database."""
    pass
