package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/lordsonvimal/polyflow/internal/ops"
)

// handle registers h at pattern on the mux, wrapped with UB.2 tool-call
// audit recording (source: "ui"). Recording failures never change the
// response; they are logged to stderr (UB.2 item 3, "never fail the call").
func (s *Server) handle(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, s.audit(pattern, h))
}

// responseRecorder captures the status and body a handler writes so audit
// can persist the full response alongside the request that produced it.
type responseRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// audit wraps h so every call is recorded as a tool_calls row: params is
// {"method","path","query","body"}, result is the full response body,
// source is "ui" (the SPA is the only sanctioned caller of /api/*).
func (s *Server) audit(pattern string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.idxMu.RLock()
		o := s.ops
		s.idxMu.RUnlock()
		if o == nil {
			h(w, r)
			return
		}

		var reqBody []byte
		if r.Body != nil {
			reqBody, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(reqBody))
		}

		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		h(rec, r)
		dur := time.Since(start)

		params := map[string]any{
			"method": r.Method,
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
		}
		if len(reqBody) > 0 {
			params["body"] = string(reqBody)
		}
		paramsJSON, _ := json.Marshal(params)

		status := "ok"
		errMsg := ""
		if rec.status >= 400 {
			status = "error"
			errMsg = rec.body.String()
		}

		call, evicted, err := o.RecordCall(r.Context(), ops.Call{
			Source:     "ui",
			Tool:       pattern,
			Params:     string(paramsJSON),
			DurationMS: dur.Milliseconds(),
			Status:     status,
			Error:      errMsg,
			Result:     rec.body.String(),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "polyflow: ops record failed: %v\n", err)
			return
		}
		s.broadcastToolCall(call, evicted)
	}
}

// broadcastToolCall pushes the live-tail SSE events UB.2 specifies:
// tool_call per recorded row, tool_call_evicted when retention trims rows.
// Non-blocking, mirroring Reload's broadcast — a slow/absent SSE client
// never blocks the request that triggered the recording.
func (s *Server) broadcastToolCall(call ops.ToolCall, evicted []int64) {
	if msg, err := json.Marshal(map[string]any{"type": "tool_call", "call": call}); err == nil {
		select {
		case s.broadcast <- string(msg):
		default:
		}
	}
	if len(evicted) > 0 {
		s.broadcastToolCallEvicted(evicted)
	}
}

func (s *Server) broadcastToolCallEvicted(ids []int64) {
	if msg, err := json.Marshal(map[string]any{"type": "tool_call_evicted", "ids": ids}); err == nil {
		select {
		case s.broadcast <- string(msg):
		default:
		}
	}
}
