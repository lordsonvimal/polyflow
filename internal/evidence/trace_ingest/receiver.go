package trace_ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Receiver is an embedded OTLP receiver that appends span exports to a
// capture session. It serves two transports concurrently:
//   - OTLP/HTTP  (POST /v1/traces, port 4318 by default)
//   - OTLP/gRPC  (TraceService/Export, port 4317 by default)
//
// Both transports accept protobuf and (for HTTP) JSON bodies. CORS is
// enabled on the HTTP port so browser-side SDKs can reach it directly.
//
// Session writes are serialised through Session.Append's mutex; the
// Receiver itself is not concurrency-safe — Start and Stop must each be
// called exactly once.
type Receiver struct {
	session  *Session
	httpPort int
	grpcPort int

	httpServer *http.Server
	grpcServer *grpc.Server
	httpLis    net.Listener
	grpcLis    net.Listener

	stopOnce sync.Once
	stopped  chan struct{}
}

// NewReceiver builds a Receiver for the given session and port numbers.
// Pass 0 for either port to get an OS-assigned port (useful in tests).
// No servers are started until Start is called.
func NewReceiver(session *Session, httpPort, grpcPort int) *Receiver {
	return &Receiver{
		session:  session,
		httpPort: httpPort,
		grpcPort: grpcPort,
		stopped:  make(chan struct{}),
	}
}

// Start binds both listeners and begins serving. Both listeners are up when
// Start returns, so callers can write pidfiles or issue test requests
// immediately. The two servers run in background goroutines; Stop shuts them.
func (r *Receiver) Start() error {
	httpLis, err := net.Listen("tcp", fmt.Sprintf(":%d", r.httpPort))
	if err != nil {
		return fmt.Errorf("receiver: bind HTTP :%d: %w", r.httpPort, err)
	}
	r.httpLis = httpLis

	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", r.grpcPort))
	if err != nil {
		_ = httpLis.Close()
		return fmt.Errorf("receiver: bind gRPC :%d: %w", r.grpcPort, err)
	}
	r.grpcLis = grpcLis

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", r.handleHTTP)
	r.httpServer = &http.Server{Handler: mux}

	r.grpcServer = grpc.NewServer()
	collectortrace.RegisterTraceServiceServer(r.grpcServer, &grpcTraceHandler{session: r.session})

	go func() { _ = r.httpServer.Serve(httpLis) }()
	go func() { _ = r.grpcServer.Serve(grpcLis) }()

	return nil
}

// HTTPPort returns the actual bound port (useful when 0 was passed to NewReceiver).
func (r *Receiver) HTTPPort() int {
	if r.httpLis == nil {
		return r.httpPort
	}
	return r.httpLis.Addr().(*net.TCPAddr).Port
}

// GRPCPort returns the actual bound gRPC port.
func (r *Receiver) GRPCPort() int {
	if r.grpcLis == nil {
		return r.grpcPort
	}
	return r.grpcLis.Addr().(*net.TCPAddr).Port
}

// Stop shuts both servers down gracefully. The session is NOT finalised
// here — the caller finalises after Stop (capture start / capture run).
func (r *Receiver) Stop() {
	r.stopOnce.Do(func() {
		if r.grpcServer != nil {
			r.grpcServer.GracefulStop()
		}
		if r.httpServer != nil {
			_ = r.httpServer.Shutdown(context.Background())
		}
		close(r.stopped)
	})
}

// Done returns a channel closed when the receiver has fully stopped.
func (r *Receiver) Done() <-chan struct{} { return r.stopped }

// ─── HTTP handler ─────────────────────────────────────────────────────────────

// handleHTTP serves POST /v1/traces with CORS support for browser SDKs.
// Accepted Content-Types: application/x-protobuf and application/json.
func (r *Receiver) handleHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if req.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	ct := req.Header.Get("Content-Type")
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = strings.TrimSpace(ct[:idx])
	}

	var line []byte
	switch ct {
	case "application/x-protobuf", "":
		// Convert to OTLP JSON so spans.otlp.json stays JSON-parseable.
		line, err = protoBodyToJSONLine(body)
		if err != nil {
			// Fall back to raw proto — ParseOTLPBytes auto-detects both formats.
			line = body
		}
	case "application/json":
		line = body
	default:
		http.Error(w, "unsupported Content-Type: "+ct, http.StatusUnsupportedMediaType)
		return
	}

	if err := r.session.Append(line); err != nil {
		http.Error(w, "append: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// protoBodyToJSONLine converts raw ExportTraceServiceRequest protobuf bytes
// to an OTLP JSON line so the session JSONL file stays JSON-parseable.
// If conversion fails, returns an error and the caller falls back to raw bytes.
func protoBodyToJSONLine(protoBytes []byte) ([]byte, error) {
	spans, err := ParseOTLPBytes(protoBytes)
	if err != nil {
		return nil, err
	}
	return spansToOTLPJSONLine(spans)
}

// spansToOTLPJSONLine serialises spans as a single-line OTLP JSON document
// suitable for appending to a JSONL session file.
func spansToOTLPJSONLine(spans []Span) ([]byte, error) {
	// Group by service preserving first-seen order for stable output.
	var svcOrder []string
	byService := map[string][]Span{}
	for _, sp := range spans {
		if _, seen := byService[sp.Service]; !seen {
			svcOrder = append(svcOrder, sp.Service)
		}
		byService[sp.Service] = append(byService[sp.Service], sp)
	}

	type attrV struct {
		StringValue string `json:"stringValue"`
	}
	type attr struct {
		Key   string `json:"key"`
		Value attrV  `json:"value"`
	}
	type jLink struct {
		TraceID string `json:"traceId"`
		SpanID  string `json:"spanId"`
	}
	type jSpan struct {
		TraceID           string  `json:"traceId"`
		SpanID            string  `json:"spanId"`
		ParentSpanID      string  `json:"parentSpanId,omitempty"`
		Name              string  `json:"name"`
		Kind              int     `json:"kind"`
		StartTimeUnixNano uint64  `json:"startTimeUnixNano"`
		EndTimeUnixNano   uint64  `json:"endTimeUnixNano"`
		Attributes        []attr  `json:"attributes"`
		Links             []jLink `json:"links,omitempty"`
	}
	type jScope struct {
		Spans []jSpan `json:"spans"`
	}
	type jRS struct {
		Resource   struct {
			Attributes []attr `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []jScope `json:"scopeSpans"`
	}
	type jReq struct {
		ResourceSpans []jRS `json:"resourceSpans"`
	}

	kindInt := func(k string) int {
		switch k {
		case "SERVER":
			return 2
		case "CLIENT":
			return 3
		case "PRODUCER":
			return 4
		case "CONSUMER":
			return 5
		default:
			return 1
		}
	}

	var rss []jRS
	for _, svc := range svcOrder {
		var jSpans []jSpan
		for _, sp := range byService[svc] {
			// Attributes: sort by key for deterministic output (bug-class rule 2).
			var attrs []attr
			for k, v := range sp.Attrs {
				attrs = append(attrs, attr{Key: k, Value: attrV{StringValue: v}})
			}
			sort.Slice(attrs, func(i, j int) bool { return attrs[i].Key < attrs[j].Key })

			var links []jLink
			for _, l := range sp.Links {
				links = append(links, jLink{TraceID: l.TraceID, SpanID: l.SpanID})
			}
			jSpans = append(jSpans, jSpan{
				TraceID:           sp.TraceID,
				SpanID:            sp.SpanID,
				ParentSpanID:      sp.ParentSpanID,
				Name:              sp.Name,
				Kind:              kindInt(sp.Kind),
				StartTimeUnixNano: sp.StartUnixNano,
				EndTimeUnixNano:   sp.EndUnixNano,
				Attributes:        attrs,
				Links:             links,
			})
		}
		rs := jRS{}
		rs.Resource.Attributes = []attr{{Key: "service.name", Value: attrV{StringValue: svc}}}
		rs.ScopeSpans = []jScope{{Spans: jSpans}}
		rss = append(rss, rs)
	}

	return json.Marshal(jReq{ResourceSpans: rss})
}

// ─── gRPC handler ────────────────────────────────────────────────────────────

type grpcTraceHandler struct {
	collectortrace.UnimplementedTraceServiceServer
	session *Session
}

// Export receives an ExportTraceServiceRequest, converts it to an OTLP JSON
// line, and appends it to the session.
func (h *grpcTraceHandler) Export(ctx context.Context, req *collectortrace.ExportTraceServiceRequest) (*collectortrace.ExportTraceServiceResponse, error) {
	// Marshal to wire format so protoBodyToJSONLine can re-parse it cleanly.
	protoBytes, err := proto.Marshal(req)
	if err != nil {
		return nil, grpcstatus.Errorf(codes.Internal, "marshal request: %v", err)
	}

	jsonLine, convErr := protoBodyToJSONLine(protoBytes)
	if convErr != nil {
		// Fall back to raw proto bytes (ParseOTLPBytes handles both).
		if appendErr := h.session.Append(protoBytes); appendErr != nil {
			return nil, grpcstatus.Errorf(codes.Internal, "append: %v", appendErr)
		}
		return &collectortrace.ExportTraceServiceResponse{}, nil
	}
	if err := h.session.Append(jsonLine); err != nil {
		return nil, grpcstatus.Errorf(codes.Internal, "append: %v", err)
	}
	return &collectortrace.ExportTraceServiceResponse{}, nil
}
