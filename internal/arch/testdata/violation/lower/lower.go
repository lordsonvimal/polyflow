// Package lower stands in for a leaf layer that must not depend upwards. It
// deliberately violates the guard's rules; nothing outside the fixture imports
// it.
package lower

import "github.com/lordsonvimal/polyflow/internal/arch/testdata/violation/mid"

// Name reaches mid directly and upper transitively.
func Name() string { return "lower/" + mid.Name() }
