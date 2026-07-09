//go:build ignore

package main

// AWS SDK v2 shapes: config-based client, context-first two-argument calls.
// These must produce ZERO matches against the v1 patterns — the entire point
// of version-split pattern files.
func uploadV2(ctx context.Context, cfg aws.Config, body io.Reader) error {
	client := s3.NewFromConfig(cfg)
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("artifacts"),
		Key:    aws.String("build.tar.gz"),
		Body:   body,
	})
	if err != nil {
		return err
	}
	_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("artifacts")})
	return err
}
