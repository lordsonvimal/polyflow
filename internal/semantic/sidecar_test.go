package semantic_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/semantic"
	"github.com/lordsonvimal/polyflow/internal/sidecar"
)

// buildStubSidecar compiles a minimal Go stub that reads length-prefixed JSON
// frames from stdin and replies with deterministic 768-dim float32 vectors.
// Each vector's values are (input_index+1)*0.001.  Skips if the Go toolchain
// is absent.
func buildStubSidecar(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not found — skipping sidecar IPC test")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "stub.go")
	bin := filepath.Join(dir, "stub-sidecar")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	code := `package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
)

func main() {
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(os.Stdin, hdr[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(hdr[:])
		payload := make([]byte, size)
		if _, err := io.ReadFull(os.Stdin, payload); err != nil {
			return
		}
		var req struct{ Texts []string }
		_ = json.Unmarshal(payload, &req)

		vecs := make([][]float32, len(req.Texts))
		for i := range vecs {
			v := make([]float32, 768)
			for d := range v {
				v[d] = float32(i+1) * 0.001
			}
			vecs[i] = v
		}
		resp, _ := json.Marshal(map[string]any{"vectors": vecs, "error": ""})
		out := make([]byte, 4+len(resp))
		binary.LittleEndian.PutUint32(out[:4], uint32(len(resp)))
		copy(out[4:], resp)
		os.Stdout.Write(out)
	}
}
`
	if err := os.WriteFile(src, []byte(code), 0o644); err != nil {
		t.Fatalf("write stub src: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("compile stub sidecar: %v", err)
	}
	return bin
}

// buildNoopBin compiles a Go program that exits immediately (no frame protocol).
func buildNoopBin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not found")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "noop.go")
	bin := filepath.Join(dir, "noop")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if err := os.WriteFile(src, []byte("package main\nfunc main() {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("compile noop: %v", err)
	}
	return bin
}

func TestSidecarEmbedder_RoundTrip(t *testing.T) {
	bin := buildStubSidecar(t)
	c, err := sidecar.StartClient(bin)
	if err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	emb := semantic.NewSidecarEmbedder(c)
	defer emb.Close()

	texts := []string{"hello world", "purchase handler"}
	vecs, err := emb.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("want 2 vectors, got %d", len(vecs))
	}
	if len(vecs[0]) != 768 {
		t.Fatalf("want 768-dim vector, got %d", len(vecs[0]))
	}
	// Stub: index 0 → 0.001, index 1 → 0.002
	if vecs[0][0] != 0.001 {
		t.Errorf("vector[0][0] = %v, want 0.001", vecs[0][0])
	}
	if vecs[1][0] != 0.002 {
		t.Errorf("vector[1][0] = %v, want 0.002", vecs[1][0])
	}
}

func TestSidecarEmbedder_IDAndDims(t *testing.T) {
	bin := buildStubSidecar(t)
	c, err := sidecar.StartClient(bin)
	if err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	emb := semantic.NewSidecarEmbedder(c)
	defer emb.Close()

	if emb.ID() != semantic.SidecarEmbedderID {
		t.Errorf("ID() = %q, want %q", emb.ID(), semantic.SidecarEmbedderID)
	}
	if emb.Dims() != semantic.SidecarEmbedderDims {
		t.Errorf("Dims() = %d, want %d", emb.Dims(), semantic.SidecarEmbedderDims)
	}
}

func TestSidecarEmbedder_DeadAfterTransportError(t *testing.T) {
	bin := buildNoopBin(t)
	c, err := sidecar.StartClient(bin)
	if err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	emb := semantic.NewSidecarEmbedder(c)
	defer emb.Close()

	_, err1 := emb.Embed(context.Background(), []string{"foo"})
	if err1 == nil {
		t.Fatal("expected error from dead sidecar, got nil")
	}
	// Subsequent call returns the same cached error (dead flag).
	_, err2 := emb.Embed(context.Background(), []string{"bar"})
	if err2 == nil {
		t.Fatal("expected error on second call to dead sidecar, got nil")
	}
}

func TestSidecarEmbedder_Determinism(t *testing.T) {
	bin := buildStubSidecar(t)

	startAndEmbed := func() [][]float32 {
		c, err := sidecar.StartClient(bin)
		if err != nil {
			t.Fatalf("start sidecar: %v", err)
		}
		emb := semantic.NewSidecarEmbedder(c)
		defer emb.Close()
		vecs, err := emb.Embed(context.Background(), []string{"foo", "bar", "baz"})
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		return vecs
	}

	vecs1 := startAndEmbed()
	vecs2 := startAndEmbed()

	j1, _ := json.Marshal(vecs1)
	j2, _ := json.Marshal(vecs2)
	if string(j1) != string(j2) {
		t.Error("two runs on same input produced different vectors (determinism violation)")
	}
}
