package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lordsonvimal/polyflow/internal/capture"
	"github.com/lordsonvimal/polyflow/internal/evidence/trace_ingest"
)

// progressInterval throttles capture_progress SSE pushes per the plan
// ("throttled ≥1 s").
const progressInterval = time.Second

// fusionHint is returned by every capture/ingest response that produced (or
// may have produced) new spans on disk — runtime evidence only fuses into
// the graph at index time (F.0 reconciler), never on capture/ingest itself.
const fusionHint = "run index to fuse this evidence into the graph"

type captureStartBody struct {
	Session  string `json:"session"`
	HTTPPort int    `json:"http_port"`
	GRPCPort int    `json:"grpc_port"`
}

// handleCaptureStart handles POST /api/capture/start
// {"session": "s1", "http_port": 4318, "grpc_port": 4317}.
func (s *Server) handleCaptureStart(w http.ResponseWriter, r *http.Request) {
	if s.capture == nil {
		writeError(w, http.StatusServiceUnavailable, "capture is not available")
		return
	}

	var body captureStartBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	name := capture.DefaultSessionName(body.Session)
	httpPort := body.HTTPPort
	if httpPort == 0 {
		httpPort = 4318
	}
	grpcPort := body.GRPCPort
	if grpcPort == 0 {
		grpcPort = 4317
	}

	h, err := s.capture.Start(name, "partial", httpPort, grpcPort)
	if err != nil {
		var portErr capture.ErrPortInUse
		if isErrPortInUse(err, &portErr) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": portErr.Error(),
				"port":  portErr.Port,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	go s.streamCaptureProgress(name)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"session":   name,
		"status":    "active",
		"http_port": h.Receiver.HTTPPort(),
		"grpc_port": h.Receiver.GRPCPort(),
	})
}

func isErrPortInUse(err error, out *capture.ErrPortInUse) bool {
	if pe, ok := err.(capture.ErrPortInUse); ok {
		*out = pe
		return true
	}
	return false
}

// streamCaptureProgress pushes capture_progress SSE events for an active
// session at most once per progressInterval, until the session is no
// longer active in this Manager (stopped from either surface — the loop
// simply observes SpanCount's second return value going false).
func (s *Server) streamCaptureProgress(name string) {
	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()
	for range ticker.C {
		n, active := s.capture.SpanCount(name)
		if !active {
			return
		}
		msg, err := json.Marshal(map[string]any{
			"type":           "capture_progress",
			"session":        name,
			"spans_received": n,
		})
		if err == nil {
			s.Broadcast(string(msg))
		}
	}
}

type captureStopBody struct {
	Session string `json:"session"`
}

// handleCaptureStop handles POST /api/capture/stop {"session": "s1"}.
func (s *Server) handleCaptureStop(w http.ResponseWriter, r *http.Request) {
	if s.capture == nil {
		writeError(w, http.StatusServiceUnavailable, "capture is not available")
		return
	}
	var body captureStopBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.Session == "" {
		writeError(w, http.StatusBadRequest, "session is required")
		return
	}

	res, err := s.capture.Stop(body.Session, "")
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session":     body.Session,
		"finalized":   res.Finalized,
		"fusion_hint": fusionHint,
	})
}

// handleCaptureStatus handles GET /api/capture/status.
func (s *Server) handleCaptureStatus(w http.ResponseWriter, r *http.Request) {
	if s.capture == nil {
		writeError(w, http.StatusServiceUnavailable, "capture is not available")
		return
	}
	status := s.capture.Status()
	if status.Active == nil {
		status.Active = []capture.ActiveStatus{}
	}
	if status.Sessions == nil {
		status.Sessions = []trace_ingest.SessionInfo{}
	}
	writeJSON(w, http.StatusOK, status)
}

// handleCaptureIngest handles POST /api/capture/ingest — either a multipart
// form upload (field "file") of an OTLP dump, or a JSON body {"path": "..."}
// naming a local file the server process can read.
func (s *Server) handleCaptureIngest(w http.ResponseWriter, r *http.Request) {
	if s.capture == nil {
		writeError(w, http.StatusServiceUnavailable, "capture is not available")
		return
	}

	ct := r.Header.Get("Content-Type")
	var session string
	var data []byte

	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "parse multipart form: "+err.Error())
			return
		}
		session = r.FormValue("session")
		file, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing multipart field \"file\": "+err.Error())
			return
		}
		defer file.Close()
		data, err = io.ReadAll(file)
		if err != nil {
			writeError(w, http.StatusBadRequest, "read upload: "+err.Error())
			return
		}
	} else {
		var body struct {
			Session string `json:"session"`
			Path    string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		if body.Path == "" {
			writeError(w, http.StatusBadRequest, "path is required when not uploading a file")
			return
		}
		session = body.Session
		raw, err := os.ReadFile(body.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("read %s: %v", body.Path, err))
			return
		}
		data = raw
	}

	name := capture.DefaultSessionName(session)
	n, err := s.capture.Ingest(name, data)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session":     name,
		"span_count":  n,
		"fusion_hint": fusionHint,
	})
}

// handleRuntimeFlows handles GET /api/runtime/flows?session=.
func (s *Server) handleRuntimeFlows(w http.ResponseWriter, r *http.Request) {
	if s.capture == nil {
		writeError(w, http.StatusServiceUnavailable, "capture is not available")
		return
	}
	session := r.URL.Query().Get("session")
	if session == "" {
		writeError(w, http.StatusBadRequest, "session is required")
		return
	}

	spans, flows, ledger, err := s.capture.Flows(session)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if spans == nil {
		spans = []trace_ingest.Span{}
	}
	if flows == nil {
		flows = []trace_ingest.FlowRecord{}
	}
	if ledger == nil {
		ledger = []trace_ingest.IngestLedgerEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session":      session,
		"spans":        spans,
		"flow_records": flows,
		"ledger":       ledger,
	})
}

// handleRuntimeCoverage handles GET /api/runtime/coverage?session= — the
// session's observed flow records compared against the indexed static edge
// baseline (verified / observed-only-gap / static-only breakdown).
func (s *Server) handleRuntimeCoverage(w http.ResponseWriter, r *http.Request) {
	if s.capture == nil {
		writeError(w, http.StatusServiceUnavailable, "capture is not available")
		return
	}
	session := r.URL.Query().Get("session")
	if session == "" {
		writeError(w, http.StatusBadRequest, "session is required")
		return
	}

	_, flows, ledger, err := s.capture.Flows(session)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	edges := trace_ingest.RuntimeCoverageEdges(idx.AllEdges())
	report := trace_ingest.ComputeSessionCoverage(flows, edges, ledger)

	writeJSON(w, http.StatusOK, map[string]any{
		"session":  session,
		"coverage": report,
	})
}
