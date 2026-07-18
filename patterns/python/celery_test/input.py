"""Celery task definitions and dispatch calls."""
from celery import shared_task
from myapp.celery import app as celery_app


# Task definitions — should match celery_task_decorator (4 variants)

@celery_app.task
def send_email(user_id, address):
    """Send a welcome email."""
    pass


@celery_app.task(bind=True, max_retries=3)
def process_payment(self, order_id, amount):
    """Process a payment with retry support."""
    pass


@shared_task
def generate_report(report_id):
    """Generate a background report."""
    pass


@shared_task(bind=True)
def export_data(self, dataset_id):
    """Export data asynchronously."""
    pass


# Dispatch calls — should match celery_task_delay and celery_apply_async

def enqueue_email(user_id, email):
    send_email.delay(user_id, email)


def schedule_payment(order_id, amount):
    process_payment.apply_async(args=[order_id, amount], countdown=60)
