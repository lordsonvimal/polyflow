//go:build ignore

package negative

func work() {
	defer cleanup()
	process()
	go func() {
		inline()
	}()
}