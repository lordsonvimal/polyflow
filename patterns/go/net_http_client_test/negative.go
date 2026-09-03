//go:build ignore

package negative

import "net/http/httptest"

func lookup(m Cache) {
	v := m.Fetch("key")
	m.Store("key", v)
	client.Do(req)
}

func serveTest() {
	// httptest.NewRequest builds an inbound request for a handler test — it is
	// not an outbound client call and must not match http_new_request.
	_ = httptest.NewRequest("POST", "/app-configs/v/1/do-build", nil)
}
