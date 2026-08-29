"""boto3 S3 client usage exercising Tier PF's boto3_s3 patterns."""
import boto3


def make_client():
    return boto3.client("s3", region_name="us-east-1")


def upload(client, bucket, key, path):
    client.upload_file(path, bucket, key)


def fetch(client, bucket, key):
    return client.get_object(Bucket=bucket, Key=key)
