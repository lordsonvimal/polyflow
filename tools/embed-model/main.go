// Command embed-model generates a synthetic PLF1 static embedding model for
// development use. For production, run convert.py against potion-base-8M.
//
// Usage: go run ./tools/embed-model [-out internal/semantic/model/model.bin]
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
)

const (
	dims   = 256
	seed   = 42
	magic  = "PLF1"
)

func main() {
	out := flag.String("out", "internal/semantic/model/model.bin", "output path")
	flag.Parse()

	vocab := buildVocab()
	nTokens := len(vocab)
	fmt.Printf("Generating synthetic model: %d tokens × %d dims\n", nTokens, dims)

	// Deterministic LCG PRNG (seed=42).  All weights are generated from this
	// single stream so the output is bit-for-bit reproducible.
	rng := &lcg{state: seed}

	// Generate fp32 matrix first, then quantize to int8 with per-token max-abs scales.
	matrix := make([]int8, nTokens*dims)
	scales := make([]float32, nTokens)
	for i := 0; i < nTokens; i++ {
		var maxAbs float32
		row := make([]float32, dims)
		for d := 0; d < dims; d++ {
			v := rng.float() // uniform in (-1, 1)
			row[d] = v
			if a := float32(math.Abs(float64(v))); a > maxAbs {
				maxAbs = a
			}
		}
		scale := float32(1.0 / 127.0)
		if maxAbs > 0 {
			scale = maxAbs / 127.0
		}
		scales[i] = scale
		inv := 1.0 / scale
		base := i * dims
		for d := 0; d < dims; d++ {
			q := int32(math.Round(float64(row[d]) * float64(inv)))
			if q > 127 {
				q = 127
			} else if q < -128 {
				q = -128
			}
			matrix[base+d] = int8(q)
		}
	}

	// Write PLF1 binary.
	if err := writePLF1(*out, vocab, matrix, scales, dims); err != nil {
		log.Fatalf("write model: %v", err)
	}
	fi, _ := os.Stat(*out)
	fmt.Printf("Wrote %s (%.1f KB)\n", *out, float64(fi.Size())/1024)
}

func writePLF1(path string, vocab []string, matrix []int8, scales []float32, d int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	n := len(vocab)
	// Header
	if _, err := f.WriteString(magic); err != nil {
		return err
	}
	if err := writeU32(f, uint32(d)); err != nil {
		return err
	}
	if err := writeU32(f, uint32(n)); err != nil {
		return err
	}
	// Vocab lengths
	for _, tok := range vocab {
		if err := writeU16(f, uint16(len(tok))); err != nil {
			return err
		}
	}
	// Vocab bytes
	for _, tok := range vocab {
		if _, err := f.WriteString(tok); err != nil {
			return err
		}
	}
	// Matrix (int8, row-major)
	raw := make([]byte, len(matrix))
	for i, v := range matrix {
		raw[i] = byte(v)
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	// Scales (float32 LE)
	scaleBuf := make([]byte, 4)
	for _, s := range scales {
		binary.LittleEndian.PutUint32(scaleBuf, math.Float32bits(s))
		if _, err := f.Write(scaleBuf); err != nil {
			return err
		}
	}
	return nil
}

func writeU32(f *os.File, v uint32) error {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	_, err := f.Write(b)
	return err
}

func writeU16(f *os.File, v uint16) error {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	_, err := f.Write(b)
	return err
}

// lcg is a simple linear congruential generator (Knuth) for deterministic weights.
type lcg struct{ state uint64 }

func (g *lcg) next() uint64 {
	g.state = g.state*6364136223846793005 + 1442695040888963407
	return g.state
}

func (g *lcg) float() float32 {
	// Map [0, 2^63) to (-1, 1)
	v := float64(g.next()>>1) / float64(1<<62)
	return float32(v*2 - 1)
}

func buildVocab() []string {
	v := []string{
		// Special tokens (indices 0-4)
		"[PAD]", "[UNK]", "[CLS]", "[SEP]", "[MASK]",
		// Common subword pieces
		"##s", "##ed", "##ing", "##er", "##ly", "##tion", "##ation",
		"##ment", "##ness", "##ful", "##able", "##ible", "##al", "##ical",
		"##ous", "##ive", "##ize", "##ise", "##ity", "##y", "##ry",
		"##age", "##ance", "##ence", "##ism", "##ist", "##or", "##en",
		"##ic", "##est", "##ary", "##ory", "##ure", "##ward", "##less",
		"##like", "##work", "##time", "##side", "##gate", "##line",
		"##way", "##out", "##in", "##up", "##down", "##over", "##under",
		"##port", "##path", "##node", "##edge", "##type", "##name",
		"##data", "##base", "##file", "##key", "##val", "##id", "##map",
		"##set", "##list", "##api", "##url", "##http", "##json", "##sql",
		// Lowercase letters (single-char fallback for unknown tokens)
		"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m",
		"n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z",
		// Digits
		"0", "1", "2", "3", "4", "5", "6", "7", "8", "9",
		// Punctuation (basic tokenizer emits these as separate tokens)
		"!", "?", ".", ",", ";", ":", "-", "/", "@", "#", "%", "&", "+",
		"=", "<", ">", "|", "~", "^", "*", "\\", "(", ")", "[", "]", "{", "}",
		// Programming keywords and identifiers
		"func", "var", "const", "type", "if", "else", "for", "range",
		"switch", "case", "default", "return", "break", "continue",
		"struct", "interface", "map", "chan", "go", "defer", "select",
		"import", "package", "error", "nil", "true", "false",
		"int", "int8", "int16", "int32", "int64", "uint", "uint8",
		"uint16", "uint32", "uint64", "float32", "float64", "bool",
		"byte", "rune", "string", "any", "make", "new", "append",
		"len", "cap", "delete", "copy", "close", "panic", "recover",
		"print", "println", "fmt", "log", "os", "io", "http", "json",
		"sql", "ctx", "err", "ok", "db", "tx", "req", "res", "resp",
		"server", "client", "handler", "router", "route", "api",
		"service", "store", "repo", "model", "query", "node", "edge",
		"graph", "file", "path", "dir", "key", "val", "id", "name",
		"label", "kind", "meta", "data", "config", "opts", "args",
		"context", "request", "response", "body", "header", "status",
		"method", "url", "host", "port", "addr", "conn", "sock",
		// Common English verbs / actions
		"create", "read", "update", "delete", "list", "get", "set",
		"add", "remove", "find", "search", "filter", "sort", "parse",
		"format", "encode", "decode", "hash", "sign", "verify", "auth",
		"check", "validate", "transform", "convert", "serialize",
		"deserialize", "marshal", "unmarshal", "render", "display",
		"show", "hide", "enable", "disable", "allow", "deny", "block",
		"permit", "reject", "accept", "connect", "disconnect", "login",
		"logout", "register", "subscribe", "publish", "emit", "send",
		"receive", "fetch", "load", "save", "write", "open", "close",
		"start", "stop", "init", "run", "execute", "process", "handle",
		"dispatch", "match", "bind", "resolve", "compile", "link",
		"build", "make", "generate", "yield", "wait", "signal", "notify",
		"alert", "deploy", "serve", "listen", "join", "leave", "push",
		"pull", "merge", "split", "copy", "clone", "move", "rename",
		"insert", "select", "where", "group", "order", "count", "sum",
		"min", "max", "avg", "distinct", "unique", "rollback", "commit",
		"migrate", "seed", "index", "cache", "flush", "evict", "expire",
		"lock", "unlock", "acquire", "release", "retry", "fallback",
		// Common nouns / entities
		"user", "account", "session", "token", "cookie", "password",
		"email", "phone", "address", "profile", "setting", "preference",
		"order", "product", "item", "cart", "checkout", "payment",
		"invoice", "receipt", "refund", "charge", "billing", "wallet",
		"balance", "transfer", "deposit", "withdraw", "credit", "debit",
		"transaction", "purchase", "sale", "price", "fee", "tax",
		"discount", "coupon", "promo", "reward", "point", "bonus",
		"customer", "merchant", "vendor", "supplier", "partner", "admin",
		"role", "permission", "policy", "rule", "scope", "group", "team",
		"organization", "company", "tenant", "workspace", "project",
		"environment", "stage", "version", "release", "branch", "commit",
		"pull", "review", "approval", "merge", "conflict", "diff", "patch",
		"issue", "bug", "feature", "task", "milestone", "sprint", "board",
		"comment", "note", "tag", "label", "category", "type", "status",
		"priority", "severity", "urgency", "impact", "risk", "cost",
		// Infrastructure / deployment
		"container", "pod", "cluster", "node", "replica", "shard",
		"partition", "leader", "follower", "primary", "secondary",
		"backup", "snapshot", "checkpoint", "archive", "restore",
		"deploy", "scale", "health", "metric", "alert", "monitor",
		"trace", "span", "log", "event", "record", "audit", "report",
		"dashboard", "chart", "graph", "table", "column", "row",
		"schema", "database", "migration", "fixture", "mock", "stub",
		"test", "spec", "suite", "bench", "bench", "corpus", "eval",
		"score", "recall", "precision", "accuracy", "quality", "baseline",
		// Protocol / transport
		"http", "https", "grpc", "rest", "graphql", "websocket", "tcp",
		"udp", "tls", "ssl", "mqtt", "amqp", "kafka", "rabbitmq", "redis",
		"postgres", "mysql", "sqlite", "mongo", "elastic", "solr",
		"queue", "topic", "channel", "stream", "pipeline", "batch",
		"event", "message", "notification", "webhook", "callback",
		"subscription", "publisher", "subscriber", "producer", "consumer",
		"broker", "exchange", "routing", "binding", "pattern", "regex",
		// Identifiers and names common in code
		"main", "handler", "middleware", "interceptor", "filter", "plugin",
		"adapter", "wrapper", "factory", "builder", "provider", "registry",
		"container", "injector", "manager", "controller", "coordinator",
		"orchestrator", "scheduler", "executor", "runner", "worker",
		"pool", "queue", "stack", "heap", "buffer", "cache", "store",
		"repository", "gateway", "proxy", "load", "balancer", "circuit",
		"breaker", "limiter", "throttle", "quota", "budget", "timeout",
		"deadline", "cancel", "context", "scope", "namespace", "package",
		"module", "library", "framework", "platform", "service", "micro",
		"mono", "repo", "workspace", "polyflow", "index", "indexer",
		"linker", "parser", "matcher", "contract", "evidence", "fusion",
		"semantic", "embed", "vector", "cosine", "similarity", "distance",
		"retrieval", "search", "lexical", "hybrid", "ranked", "scored",
		"flow", "chain", "path", "trace", "impact", "coverage", "recall",
		// File/path components
		"internal", "cmd", "pkg", "vendor", "test", "docs", "tools",
		"web", "static", "assets", "templates", "scripts", "config",
		"etc", "var", "tmp", "bin", "lib", "src", "dist", "build",
		"go", "js", "ts", "rb", "py", "java", "cs", "php", "rs",
		"yaml", "json", "toml", "env", "md", "txt", "csv", "xml",
	}
	// Deduplicate while preserving order.
	seen := make(map[string]bool, len(v))
	out := make([]string, 0, len(v))
	for _, tok := range v {
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}
