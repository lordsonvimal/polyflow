"""Non-boto3 call sites that superficially resemble the S3 patterns."""


def make_client():
    return acme.client("s3")


def call_put_variant(obj):
    return obj.put_object_extra(Bucket="b")


def call_get_objects_plural(obj):
    return obj.get_objects(Bucket="b")
