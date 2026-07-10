//go:build ignore

package main

func run() {
	go worker()
	go obj.process()
	go func() {
		inline()
	}()
}
