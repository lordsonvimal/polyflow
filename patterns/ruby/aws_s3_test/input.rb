client = Aws::S3::Client.new(region: "us-east-1")
client.put_object(bucket: "reports", key: "monthly.pdf", body: file)
client.get_object(bucket: "reports", key: "monthly.pdf")
obj.upload_file("/tmp/report.pdf")
