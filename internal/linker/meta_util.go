package linker

// ensureMeta returns m, or a fresh empty map if m is nil — shared by the
// host-resolution passes that write into a node's Meta (js_http_hosts.go,
// js_local_url.go, schema_url_link.go). Split out of the retired
// hints.go (FX.8.31 migrated ApplyHints off Tier FX) since those other
// callers still need it.
func ensureMeta(m map[string]string) map[string]string {
	if m == nil {
		return make(map[string]string)
	}
	return m
}
