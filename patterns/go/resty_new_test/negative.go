//go:build ignore

package main

func setup() {
	// Not resty.New()
	client := http.Client{}
	_ = client

	// resty method call but not New()
	resp, _ := resty.New().Get("/users")
	_ = resp
}
