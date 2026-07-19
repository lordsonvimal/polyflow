// polyflow-parse-templ is the V.2 parser sidecar for the a-h/templ engine.
// It speaks the length-prefixed JSON parse protocol on stdio: one
// ParseRequest frame in, one ParseResponse frame out, until stdin EOF.
// This build is compiled against the templ version pinned in go.mod; future
// engine versions become additional build targets routed by resolved
// version — never surgery on shared code.
//
// Responses are sorted (nodes/edges by ID, unresolved by file/line/kind/
// name) before framing so identical parses produce byte-identical frames.
// All diagnostics go to stderr (captured by the parent, never the user's
// terminal); stdout carries protocol frames only.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/sidecar"
)

func main() {
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	for {
		payload, err := sidecar.ReadFrame(in)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "polyflow-parse-templ: read frame: %v\n", err)
			os.Exit(1)
		}
		resp := handle(payload)
		respBytes, err := json.Marshal(resp)
		if err != nil {
			respBytes, _ = json.Marshal(&sidecar.ParseResponse{Error: fmt.Sprintf("marshal response: %v", err)})
		}
		if err := sidecar.WriteFrame(out, respBytes); err != nil {
			fmt.Fprintf(os.Stderr, "polyflow-parse-templ: write frame: %v\n", err)
			os.Exit(1)
		}
		if err := out.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "polyflow-parse-templ: flush: %v\n", err)
			os.Exit(1)
		}
	}
}

// handle parses one request. Every failure — including a panic in the templ
// parser — becomes a ParseResponse.Error so the parent falls back in-process
// instead of losing the file.
func handle(payload []byte) (resp *sidecar.ParseResponse) {
	resp = &sidecar.ParseResponse{}
	defer func() {
		if r := recover(); r != nil {
			resp.Nodes, resp.Edges, resp.Unresolved = nil, nil, nil
			resp.Error = fmt.Sprintf("panic: %v", r)
		}
	}()

	var req sidecar.ParseRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		resp.Error = fmt.Sprintf("unmarshal request: %v", err)
		return resp
	}
	if req.Tool != "templ" {
		resp.Error = fmt.Sprintf("unsupported tool %q (this sidecar parses templ only)", req.Tool)
		return resp
	}
	content, err := base64.StdEncoding.DecodeString(req.ContentB64)
	if err != nil {
		resp.Error = fmt.Sprintf("decode content_b64: %v", err)
		return resp
	}

	p := &parser.TemplParser{}
	nodes, edges, unresolved, err := p.ParseSource(req.File, req.Service, content, req.DatastarVariant)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	resp.Nodes, resp.Edges, resp.Unresolved = nodes, edges, unresolved
	resp.Sort()
	return resp
}
