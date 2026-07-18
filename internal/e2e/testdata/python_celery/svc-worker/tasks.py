"""Celery worker task definitions."""
from celery import Celery, shared_task

app = Celery("worker", broker="redis://localhost:6379/0")


@app.task
def send_email(user_id, address):
    """Send a transactional email."""
    pass


@app.task(bind=True, max_retries=3)
def process_order(self, order_id, amount):
    """Process a customer order with retry support."""
    pass


@shared_task
def generate_report(report_id):
    """Generate a background report asynchronously."""
    pass
