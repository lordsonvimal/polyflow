// Package pluginloader is core's half of docs/linker-plugin-architecture-plan.md:
// manifest discovery, the go-plugin subprocess handshake, and (in
// capabilities.go) the bulk-query capability implementations a plugin
// component can request. It is the only package outside sdk/linkplugin that
// speaks the plugin wire protocol.
package pluginloader

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/sdk/linkplugin"
)

// PluginsDirEnv names the environment variable pointing at a user-wide
// plugins directory, as opposed to a project-local .polyflow/plugins/
// checked into a repo.
const PluginsDirEnv = "POLYFLOW_PLUGINS_DIR"

// projectPluginsDir is the project-local override directory, relative to a
// workspace's repo root — see the plan's "Directory + manifest convention".
const projectPluginsDir = ".polyflow/plugins"

// Component is one manifest.yaml components[] entry.
type Component struct {
	ID             string   `yaml:"id"`
	Package        string   `yaml:"package"`
	VersionRange   string   `yaml:"version_range"`
	Language       string   `yaml:"language"`
	Patterns       string   `yaml:"patterns"`
	Requires       []string `yaml:"requires"`
	ConcurrentSafe bool     `yaml:"concurrent_safe"`
}

// Manifest is one plugin's manifest.yaml.
type Manifest struct {
	Name            string      `yaml:"name"`
	Version         string      `yaml:"version"`
	ProtocolVersion int         `yaml:"protocol_version"`
	Entrypoint      string      `yaml:"entrypoint"`
	Components      []Component `yaml:"components"`

	// Dir is the manifest's containing directory. Not part of the YAML;
	// LoadManifest sets it so Entrypoint/Patterns (both relative paths in
	// manifest.yaml) can be resolved without every caller re-deriving it.
	Dir string `yaml:"-"`
}

// EntrypointPath resolves Entrypoint against the manifest's directory.
func (m *Manifest) EntrypointPath() string {
	return filepath.Join(m.Dir, m.Entrypoint)
}

// PatternsPath resolves one component's Patterns against the manifest's
// directory.
func (m *Manifest) PatternsPath(c Component) string {
	return filepath.Join(m.Dir, c.Patterns)
}

// LoadManifest reads and parses one manifest.yaml.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pluginloader: read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("pluginloader: parse manifest %s: %w", path, err)
	}
	m.Dir = filepath.Dir(path)
	return &m, nil
}

// Discover finds every manifest.yaml under $POLYFLOW_PLUGINS_DIR (default
// ~/.polyflow/plugins) and <repoRoot>/.polyflow/plugins — step 1 of the
// plan's load sequence. repoRoot may be empty (user-wide plugins only).
func Discover(repoRoot string) ([]string, error) {
	var dirs []string
	if d := os.Getenv(PluginsDirEnv); d != "" {
		dirs = append(dirs, d)
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".polyflow", "plugins"))
	}
	if repoRoot != "" {
		dirs = append(dirs, filepath.Join(repoRoot, projectPluginsDir))
	}

	var manifests []string
	for _, dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*", "manifest.yaml"))
		if err != nil {
			return nil, fmt.Errorf("pluginloader: glob %s: %w", dir, err)
		}
		manifests = append(manifests, matches...)
	}
	return manifests, nil
}

// CoverageNote records why a manifest or component was skipped rather than
// invoked, per the "no silent gaps" contract (docs/phases.md) — surfaced via
// `polyflow doctor` once that integration lands (Phase 2 wires
// version_range; this type is the shape both phases share).
type CoverageNote struct {
	Plugin    string
	Component string
	Reason    string
}

// CheckProtocolVersion is step 2 of the load sequence: a manifest whose
// protocol_version core doesn't speak is skipped whole — never a hard
// failure, never a silent one either.
func CheckProtocolVersion(m *Manifest) *CoverageNote {
	if m.ProtocolVersion != linkplugin.ProtocolVersion {
		return &CoverageNote{
			Plugin: m.Name,
			Reason: fmt.Sprintf("protocol_version %d unsupported (core speaks %d)", m.ProtocolVersion, linkplugin.ProtocolVersion),
		}
	}
	return nil
}

// LaunchedPlugin is a running plugin subprocess, ready for Link/Reconcile
// calls. Callers must call Close to terminate the subprocess.
type LaunchedPlugin struct {
	Name   string
	Client *linkplugin.Client

	proc *goplugin.Client
}

// Launch spawns a manifest's entrypoint as a subprocess (step 6 of the load
// sequence — once per plugin per index run) and performs the protocol
// handshake. Callers must have already passed CheckProtocolVersion and
// confirmed at least one component qualified for at least one service.
func Launch(m *Manifest) (*LaunchedPlugin, error) {
	proc := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  linkplugin.Handshake,
		Plugins:          map[string]goplugin.Plugin{linkplugin.PluginMapKey: &linkplugin.HostPlugin{}},
		Cmd:              exec.Command(m.EntrypointPath()),
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		Logger:           hclog.NewNullLogger(),
	})

	rpcClient, err := proc.Client()
	if err != nil {
		proc.Kill()
		return nil, fmt.Errorf("pluginloader: connect to %s: %w", m.Name, err)
	}
	raw, err := rpcClient.Dispense(linkplugin.PluginMapKey)
	if err != nil {
		proc.Kill()
		return nil, fmt.Errorf("pluginloader: dispense %s: %w", m.Name, err)
	}
	client, ok := raw.(*linkplugin.Client)
	if !ok {
		proc.Kill()
		return nil, fmt.Errorf("pluginloader: %s did not return a *linkplugin.Client", m.Name)
	}

	name, err := client.Handshake(context.Background())
	if err != nil {
		proc.Kill()
		return nil, fmt.Errorf("pluginloader: handshake with %s: %w", m.Name, err)
	}

	return &LaunchedPlugin{Name: name, Client: client, proc: proc}, nil
}

// Close terminates the plugin subprocess.
func (l *LaunchedPlugin) Close() {
	l.proc.Kill()
}
