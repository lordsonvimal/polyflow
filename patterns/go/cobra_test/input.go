//go:build ignore

package main

var serveCmd = &cobra.Command{
	Use:  "serve",
	RunE: runServe,
}
