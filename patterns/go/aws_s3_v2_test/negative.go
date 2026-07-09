//go:build ignore

package main

// AWS SDK v1 shapes: session-based client, single-argument context-less
// calls. Must produce ZERO matches against the v2 patterns.
func uploadV1(sess *session.Session, body io.Reader) error {
	svc := s3.New(sess)
	_, err := svc.PutObject(&s3.PutObjectInput{
		Bucket: aws.String("artifacts"),
		Key:    aws.String("build.tar.gz"),
		Body:   body,
	})
	return err
}
