"""pika AMQP publish, consume, and queue_declare patterns."""
import pika

connection = pika.BlockingConnection(pika.ConnectionParameters('localhost'))
channel = connection.channel()


# pika_queue_declare
channel.queue_declare('task_queue')


# pika_basic_publish — positional arg form (exchange then routing_key)
channel.basic_publish('notifications', 'email.send', b'{"user": 1}')
channel.basic_publish('orders', 'order.placed', b'{"order": 42}')


# pika_basic_consume
channel.basic_consume('task_queue', on_message_callback=None)
channel.basic_consume('result_queue', on_message_callback=None)
