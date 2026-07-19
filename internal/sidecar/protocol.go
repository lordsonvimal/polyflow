// Package sidecar implements version-isolated parser sidecars (V.2): a
// payload-generic length-prefixed JSON frame layer over stdio, a pooled
// long-lived process client, and a router that dispatches files to the
// sidecar backend matching the resolved toolchain version, with graceful
// in-process fallback. The frame layer is message-schema agnostic — the
// parse schema in router.go is one instance; the semantic-search embedding
// sidecar reuses this file unchanged.
package sidecar

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// MaxFrameSize caps a single frame payload. A header announcing more is a
// protocol error (reject, not OOM).
const MaxFrameSize = 64 << 20 // 64 MiB

// WriteFrame writes one length-prefixed frame: uint32 little-endian payload
// byte length followed by the payload. Header and payload are written as one
// buffer so concurrent-writer interleaving cannot split a frame.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("sidecar: frame payload %d bytes exceeds cap %d", len(payload), MaxFrameSize)
	}
	buf := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)
	// io.Writer contract: Write returns an error whenever n < len(buf), so a
	// single call is a full-buffer write.
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one length-prefixed frame. Partial reads are the norm on
// pipes, so both the header and the payload use io.ReadFull.
func ReadFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size > MaxFrameSize {
		return nil, fmt.Errorf("sidecar: frame header announces %d bytes, exceeds cap %d", size, MaxFrameSize)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// stderrCap bounds how much sidecar stderr is retained for coverage notes.
const stderrCap = 16 << 10

// boundedBuffer keeps the first stderrCap bytes written and drops the rest;
// sidecar stderr is captured for diagnostics, never inherited to the
// user's terminal.
type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if remaining := stderrCap - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Client is one long-lived sidecar process. Requests are serialized per
// process (one in-flight frame at a time); any transport error marks the
// client dead — callers fall back in-process, they never retry the pipe.
type Client struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.Reader
	stderr  *boundedBuffer
	dead    bool
	deadErr error
}

// StartClient launches the sidecar binary at path and wires its stdio.
func StartClient(path string) (*Client, error) {
	cmd := exec.Command(path)
	stderr := &boundedBuffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("sidecar %s: stdin pipe: %w", path, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("sidecar %s: stdout pipe: %w", path, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sidecar %s: start: %w", path, err)
	}
	return &Client{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

// RoundTrip sends one request frame and reads one response frame. Serialized
// per client. A dead client returns its original transport error immediately.
func (c *Client) RoundTrip(payload []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return nil, c.deadErr
	}
	if err := WriteFrame(c.stdin, payload); err != nil {
		return nil, c.kill(fmt.Errorf("sidecar: write frame: %w", err))
	}
	resp, err := ReadFrame(c.stdout)
	if err != nil {
		return nil, c.kill(fmt.Errorf("sidecar: read frame: %w", err))
	}
	return resp, nil
}

// kill marks the client dead and reaps the process. Caller holds c.mu.
func (c *Client) kill(cause error) error {
	c.dead = true
	c.deadErr = cause
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
	return cause
}

// Stderr returns the captured (bounded) stderr output of the process.
func (c *Client) Stderr() string { return c.stderr.String() }

// Close shuts the process down. The sidecar is stateless, so an outright
// kill is safe and — unlike waiting for an EOF-triggered exit — cannot hang
// the indexer's shutdown path.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return
	}
	_ = c.kill(fmt.Errorf("sidecar: client closed"))
}
