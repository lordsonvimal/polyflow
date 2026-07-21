package main

// Map applies fn to every element of s; requires Go 1.18+ generics.
func Map[T, U any](s []T, fn func(T) U) []U {
	out := make([]U, len(s))
	for i, v := range s {
		out[i] = fn(v)
	}
	return out
}

func helper() int { return 1 }

func main() {
	_ = helper()
}
