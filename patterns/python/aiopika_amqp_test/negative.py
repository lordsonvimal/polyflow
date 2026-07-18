"""No aio-pika patterns — regular async functions without .publish() or .iterator() calls."""
import asyncio


async def send_notification(recipient, message):
    """Plain async function — not an aio-pika publisher."""
    return {"to": recipient, "body": message}


async def fetch_events(source):
    """Plain async function — not an aio-pika consumer."""
    return []


class EventEmitter:
    async def emit(self, event, payload):
        """emit is not aio-pika publish."""
        pass

    async def listen(self, topic):
        """listen is not aio-pika consume."""
        pass

    def get_messages(self):
        """get_messages returns a list, not an aio-pika iterator."""
        return list(self._messages)


async def process_list():
    emitter = EventEmitter()
    messages = emitter.get_messages()
    # Iterating a list — NOT queue.iterator()
    for msg in messages:
        print(msg)
