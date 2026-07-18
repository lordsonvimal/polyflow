"""No pika AMQP patterns — plain Python code without pika method calls.

Avoids .basic_publish(), .basic_consume(), and .queue_declare() calls so
that fixture tests confirm zero false positives when the pika package gate
is not applied (fixture tests use unversioned matchers).
"""


def send_message(recipient, text):
    """Regular function — not a message broker publish."""
    return {"to": recipient, "body": text}


def receive_messages(topic):
    """Regular function — not a message broker subscribe."""
    return []


class EventBus:
    def publish(self, event_type, payload):
        """EventBus.publish is NOT pika.Channel.basic_publish."""
        pass

    def subscribe(self, event_type, handler):
        """EventBus.subscribe is NOT pika basic_consume."""
        pass

    def declare_queue(self, name):
        """EventBus.declare_queue is NOT pika queue_declare."""
        pass


bus = EventBus()
bus.publish("order.created", {})
bus.subscribe("order.created", lambda e: None)
bus.declare_queue("orders")
