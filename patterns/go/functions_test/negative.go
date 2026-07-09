//go:build ignore

package negative

// Only type/const/var declarations — no function or method declarations.
type Config struct {
	Name string
	Port int
}

const timeout = 30

var defaultConfig = Config{Name: "dev", Port: 8080}