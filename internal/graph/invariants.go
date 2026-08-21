package graph

// AssertServiceScopedIDs returns the first (nodeID, serviceA, serviceB) triple where the same
// node ID was produced by two different services — a violation of the merge precondition that
// FR.3's per-service DB merge depends on (every node ID is already globally unique because the
// ID hash includes Service; this only asserts that invariant instead of assuming it silently).
// Called by the merge step (FR.3) before every merge; also run as a standalone `polyflow doctor`
// check so a violation surfaces before a merge silently drops data.
func AssertServiceScopedIDs(services map[string][]*Node) (nodeID, serviceA, serviceB string, ok bool) {
	seen := make(map[string]string, len(services))
	for service, nodes := range services {
		for _, n := range nodes {
			if owner, exists := seen[n.ID]; exists && owner != service {
				return n.ID, owner, service, false
			}
			seen[n.ID] = service
		}
	}
	return "", "", "", true
}
