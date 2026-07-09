//go:build ignore

package main

func upload(ctx context.Context, cfg aws.Config, body io.Reader) error {
	client := s3.NewFromConfig(cfg)
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("artifacts"),
		Key:    aws.String("build.tar.gz"),
		Body:   body,
	})
	if err != nil {
		return err
	}
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("artifacts")})
	return err
}
