//go:build ignore

package main

func upload(sess *session.Session, body io.Reader) error {
	svc := s3.New(sess)
	_, err := svc.PutObject(&s3.PutObjectInput{
		Bucket: aws.String("artifacts"),
		Key:    aws.String("build.tar.gz"),
		Body:   body,
	})
	if err != nil {
		return err
	}
	out, err := svc.GetObject(&s3.GetObjectInput{Bucket: aws.String("artifacts"), Key: aws.String("k")})
	_ = out
	return err
}
