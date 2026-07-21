package main

func transform(x int) int {
	return x * 2
}

func accumulate(vals []int) int {
	total := 0
	for _, v := range vals {
		total += transform(v)
	}
	return total
}

func main() {
	_ = accumulate([]int{1, 2, 3})
}
