"""Django ORM call sites exercising Tier PF's django_orm patterns."""
from django.db import models


class Order(models.Model):
    pass


def get_orders():
    return Order.objects.filter(status="paid")


def get_order(order_id):
    return Order.objects.get(pk=order_id)


def create_order(data):
    return Order.objects.create(**data)


def bulk_import(rows):
    Order.objects.bulk_create(rows)
