package workspace

import (
	"os"
	"path/filepath"
	"strings"
)

// FrameworkHint describes a detected framework in a directory.
type FrameworkHint struct {
	Name       string
	Language   string
	Confidence float64 // 0.0-1.0
}

// DetectFrameworks inspects path and returns the detected language and frameworks.
// It reads go.mod, package.json, and Gemfile to identify specific frameworks in use.
func DetectFrameworks(path string) ([]FrameworkHint, error) {
	var hints []FrameworkHint

	if h := detectGo(path); h != nil {
		hints = append(hints, h...)
	}
	if h := detectNode(path); h != nil {
		hints = append(hints, h...)
	}
	if h := detectRuby(path); h != nil {
		hints = append(hints, h...)
	}

	return hints, nil
}

func detectGo(path string) []FrameworkHint {
	data, err := os.ReadFile(filepath.Join(path, "go.mod"))
	if err != nil {
		return nil
	}
	hints := []FrameworkHint{{Name: "go-module", Language: "go", Confidence: 1.0}}
	content := string(data)

	goFrameworks := []struct{ pkg, name string }{
		{"github.com/go-chi/chi", "chi"},
		{"github.com/a-h/templ", "templ"},
		{"github.com/starfederation/datastar", "datastar"},
		{"github.com/rabbitmq/amqp091-go", "amqp091"},
		{"github.com/go-resty/resty", "resty"},
		{"github.com/gin-gonic/gin", "gin"},
		{"github.com/labstack/echo", "echo"},
		{"github.com/gorilla/mux", "gorilla-mux"},
		{"google.golang.org/grpc", "grpc"},
		{"github.com/nats-io/nats.go", "nats"},
		{"github.com/segmentio/kafka-go", "kafka"},
	}
	for _, fw := range goFrameworks {
		if strings.Contains(content, fw.pkg) {
			hints = append(hints, FrameworkHint{Name: fw.name, Language: "go", Confidence: 1.0})
		}
	}
	return hints
}

func detectNode(path string) []FrameworkHint {
	data, err := os.ReadFile(filepath.Join(path, "package.json"))
	if err != nil {
		return nil
	}
	// Detect TypeScript vs JavaScript by presence of tsconfig.json
	lang := "javascript"
	if _, err := os.Stat(filepath.Join(path, "tsconfig.json")); err == nil {
		lang = "typescript"
	}
	hints := []FrameworkHint{{Name: "node", Language: lang, Confidence: 1.0}}
	content := string(data)

	jsFrameworks := []struct{ pkg, name string }{
		{`"axios"`, "axios"},
		{`"react"`, "react"},
		{`"solid-js"`, "solid"},
		{`"vue"`, "vue"},
		{`"svelte"`, "svelte"},
		{`"next"`, "next"},
		{`"express"`, "express"},
		{`"fastify"`, "fastify"},
		{`"@datastar"`, "datastar"},
		{`"socket.io"`, "socket.io"},
	}
	for _, fw := range jsFrameworks {
		if strings.Contains(content, fw.pkg) {
			hints = append(hints, FrameworkHint{Name: fw.name, Language: lang, Confidence: 1.0})
		}
	}
	return hints
}

func detectRuby(path string) []FrameworkHint {
	data, err := os.ReadFile(filepath.Join(path, "Gemfile"))
	if err != nil {
		return nil
	}
	hints := []FrameworkHint{{Name: "bundler", Language: "ruby", Confidence: 1.0}}
	content := string(data)

	rubyFrameworks := []struct{ gem, name string }{
		{"rails", "rails"},
		{"sinatra", "sinatra"},
		{"sidekiq", "sidekiq"},
		{"bunny", "bunny"},
		{"httparty", "httparty"},
		{"faraday", "faraday"},
		{"rest-client", "rest_client"},
		{"pusher", "pusher"},
		{"grape", "grape"},
	}
	for _, fw := range rubyFrameworks {
		if strings.Contains(content, fw.gem) {
			hints = append(hints, FrameworkHint{Name: fw.name, Language: "ruby", Confidence: 1.0})
		}
	}
	return hints
}
