//go:build ignore

package main

func fetch(client *resty.Client) {
	client.Get("/api/users")
	client.Post("/api/users")
}

// X.1a: dynamic (non-literal) URL argument, direct chain and variable-bound
// chain — resty_get's #match? gate requires a leading quote/backtick and
// silently produced zero nodes for these before X.1a.
func fetchDynamic(buildID string) {
	resty.New().R().Get(fmt.Sprintf("%s/api/v1/builds/%s", cfg.AgentURL, buildID))
}

func fetchDynamicAlias(client *resty.Client, buildID string) {
	client.R().Get(fmt.Sprintf("%s/api/v1/builds/%s", cfg.AgentURL, buildID))
}
