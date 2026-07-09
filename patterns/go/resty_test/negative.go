//go:build ignore

package negative

func run(client Client) {
	client.Get("users")       // no leading / or scheme — not a URL
	client.Execute(req)       // wrong arity
	cache.Delete("stale-key") // not a URL either
}