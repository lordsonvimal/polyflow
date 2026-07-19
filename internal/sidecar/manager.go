package sidecar

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// SidecarDirEnv overrides where sidecar binaries are looked up. Used by tests
// (point at a build dir, or at an empty dir to force the fallback path) and
// by users with non-standard install layouts.
const SidecarDirEnv = "POLYFLOW_SIDECAR_DIR"

// backendBinaries maps a registry SidecarBackend id to its binary name.
// Adding a sidecar build = one row here + one registry row (additive).
var backendBinaries = map[string]string{
	"templ-v0.3": "polyflow-parse-templ",
}

// Manager pools one long-lived Client per sidecar backend for the duration
// of an index run. A backend whose binary is missing or fails to start is
// remembered as failed — callers fall back in-process for the rest of the
// run instead of retrying the spawn per file.
type Manager struct {
	mu      sync.Mutex
	dir     string // explicit search dir; "" → env, executable dir, then PATH
	clients map[string]*Client
	failed  map[string]error
}

// NewManager creates a Manager. dir overrides binary lookup; when empty the
// POLYFLOW_SIDECAR_DIR env var, the running executable's directory, and PATH
// are searched in that order.
func NewManager(dir string) *Manager {
	return &Manager{dir: dir, clients: map[string]*Client{}, failed: map[string]error{}}
}

// Client returns the pooled client for a backend id, starting it on first
// use. A missing binary or failed start is cached and returned for every
// subsequent call (never respawned mid-run).
func (m *Manager) Client(backend string) (*Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[backend]; ok {
		return c, nil
	}
	if err, ok := m.failed[backend]; ok {
		return nil, err
	}
	path, err := m.lookup(backend)
	if err == nil {
		var c *Client
		c, err = StartClient(path)
		if err == nil {
			m.clients[backend] = c
			return c, nil
		}
	}
	m.failed[backend] = err
	return nil, err
}

// lookup resolves the binary path for a backend id.
func (m *Manager) lookup(backend string) (string, error) {
	bin, ok := backendBinaries[backend]
	if !ok {
		return "", fmt.Errorf("sidecar: unknown backend %q", backend)
	}
	var dirs []string
	if m.dir != "" {
		dirs = append(dirs, m.dir)
	} else {
		if env := os.Getenv(SidecarDirEnv); env != "" {
			dirs = append(dirs, env)
		}
		if exe, err := os.Executable(); err == nil {
			dirs = append(dirs, filepath.Dir(exe))
		}
	}
	for _, d := range dirs {
		p := filepath.Join(d, bin)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	if m.dir == "" {
		if p, err := exec.LookPath(bin); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("sidecar: binary %s for backend %q not found (searched %v and PATH)", bin, backend, dirs)
}

// Shutdown closes every pooled client.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.Close()
	}
	m.clients = map[string]*Client{}
}
