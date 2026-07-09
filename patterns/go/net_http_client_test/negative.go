//go:build ignore

package negative

func lookup(m Cache) {
	v := m.Fetch("key")
	m.Store("key", v)
	client.Do(req)
}