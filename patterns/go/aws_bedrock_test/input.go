//go:build ignore

package main

func askModel(ctx context.Context, cfg aws.Config, prompt []byte) error {
	client := bedrockruntime.NewFromConfig(cfg)
	out, err := client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId: aws.String("anthropic.claude-3"),
		Body:    prompt,
	})
	_ = out
	stream, err2 := client.InvokeModelWithResponseStream(ctx, &bedrockruntime.InvokeModelWithResponseStreamInput{})
	_, _ = stream, err2
	return err
}
