"""aio-pika async AMQP patterns."""
import asyncio
import aio_pika


async def send_messages(channel):
    exchange = await channel.get_exchange("logs")
    # aiopika_publish — routing_key keyword arg
    await exchange.publish(
        aio_pika.Message(b"hello"),
        routing_key="info.log",
    )
    await exchange.publish(
        aio_pika.Message(b"error"),
        routing_key="error.log",
    )


async def consume_messages(queue):
    # aiopika_consume — async for message in queue.iterator()
    async for message in queue.iterator():
        async with message.process():
            print(message.body)
