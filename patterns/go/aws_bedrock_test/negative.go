//go:build ignore

package main

// S3 shapes (both SDK generations) must not match bedrock patterns —
// bedrock is a distinct LLM/AI external service, not generic cloud storage.
func s3Stuff(ctx context.Context, cfg aws.Config, sess *session.Session) {
	c2 := s3.NewFromConfig(cfg)
	c2.PutObject(ctx, &s3.PutObjectInput{})
	svc := s3.New(sess)
	svc.GetObject(&s3.GetObjectInput{})
	rt.Invoke(payload)
}
