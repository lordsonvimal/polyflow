// Package mid sits between lower and upper so the fixture also exercises a
// violation that is only reachable transitively.
package mid

import "github.com/lordsonvimal/polyflow/internal/arch/testdata/violation/upper"

// Name reaches upper, making lower -> mid -> upper a transitive violation.
func Name() string { return "mid/" + upper.Name() }
