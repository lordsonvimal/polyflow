"""Non-boto3 call sites that superficially resemble the S3 patterns."""


def make_client():
    return acme.client("s3")


def call_put_variant(obj):
    return obj.put_object_extra(Bucket="b")


def call_get_objects_plural(obj):
    return obj.get_objects(Bucket="b")


def enqueue_via_kwargs_spread(client, kwargs):
    # PW.3 non-goal: QueueUrl built into a dict and splatted in, not a
    # literal keyword_argument node at the call site — must not match.
    return client.send_message(**kwargs)


def receive_without_queue_url(client):
    return client.receive_message(MaxNumberOfMessages=1)
