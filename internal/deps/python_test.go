package deps

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvePython_RequirementsTxt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", `
# production deps
flask==2.3.0
requests==2.31.0
Werkzeug>=2.0.0
SQLAlchemy[asyncio]==2.0.21
-r requirements-extra.txt
--constraint constraints.txt

# git dep skipped
git+https://github.com/org/pkg.git
`)
	writeFile(t, dir, "requirements-dev.txt", `
pytest==7.4.0
black==23.7.0
`)

	ds, err := Resolve(dir)
	require.NoError(t, err)

	flask := find(ds, "flask")
	require.NotNil(t, flask, "flask must be found")
	assert.Equal(t, "2.3.0", flask.Version)
	assert.Equal(t, EcosystemPyPI, flask.Ecosystem)
	assert.Equal(t, KindProd, flask.Kind)

	req := find(ds, "requests")
	require.NotNil(t, req)
	assert.Equal(t, "2.31.0", req.Version)
	assert.Equal(t, KindProd, req.Kind)

	// name normalization: Werkzeug → werkzeug, best-effort version
	werk := find(ds, "werkzeug")
	require.NotNil(t, werk)
	assert.Equal(t, "2.0.0", werk.Version)

	// extras stripped from name
	sa := find(ds, "sqlalchemy")
	require.NotNil(t, sa)
	assert.Equal(t, "2.0.21", sa.Version)

	// dev file
	pytest := find(ds, "pytest")
	require.NotNil(t, pytest)
	assert.Equal(t, "7.4.0", pytest.Version)
	assert.Equal(t, KindDev, pytest.Kind)

	black := find(ds, "black")
	require.NotNil(t, black)
	assert.Equal(t, KindDev, black.Kind)

	// git dep not recorded
	assert.Nil(t, findByEcosystem(ds, EcosystemPyPI, "pkg"))
}

func TestResolvePython_PoetryLock(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "poetry.lock", `[[package]]
name = "flask"
version = "2.3.0"
description = "A simple framework"
category = "main"
optional = false
python-versions = ">=3.8"

[[package]]
name = "pytest"
version = "7.4.0"
description = "pytest: simple powerful testing"
category = "dev"
optional = false
python-versions = ">=3.7"

[[package]]
name = "requests"
version = "2.31.0"
description = "Python HTTP for Humans"
category = "main"
optional = false
`)

	ds, err := Resolve(dir)
	require.NoError(t, err)

	flask := find(ds, "flask")
	require.NotNil(t, flask)
	assert.Equal(t, "2.3.0", flask.Version)
	assert.Equal(t, EcosystemPyPI, flask.Ecosystem)
	assert.Equal(t, KindProd, flask.Kind)

	pytest := find(ds, "pytest")
	require.NotNil(t, pytest)
	assert.Equal(t, "7.4.0", pytest.Version)
	assert.Equal(t, KindDev, pytest.Kind)

	req := find(ds, "requests")
	require.NotNil(t, req)
	assert.Equal(t, KindProd, req.Kind)
}

func TestResolvePython_PoetryLock_V2Groups(t *testing.T) {
	dir := t.TempDir()
	// poetry v2 lock format: no category, uses groups = [...]
	writeFile(t, dir, "poetry.lock", `[[package]]
name = "flask"
version = "3.0.0"
groups = ["main"]

[[package]]
name = "pytest"
version = "8.0.0"
groups = ["dev"]
`)

	ds, err := Resolve(dir)
	require.NoError(t, err)

	flask := find(ds, "flask")
	require.NotNil(t, flask)
	assert.Equal(t, KindProd, flask.Kind)

	pytest := find(ds, "pytest")
	require.NotNil(t, pytest)
	assert.Equal(t, KindDev, pytest.Kind)
}

func TestResolvePython_UVLock(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "uv.lock", `version = 1
requires-python = ">=3.11"

[[package]]
name = "flask"
version = "2.3.0"
source = { registry = "https://pypi.org/simple" }

[[package]]
name = "pytest"
version = "7.4.0"
source = { registry = "https://pypi.org/simple" }

[[package]]
name = "httpx"
version = "0.27.0"
source = { registry = "https://pypi.org/simple" }
`)
	writeFile(t, dir, "pyproject.toml", `[project]
name = "myapp"
requires-python = ">=3.11"
dependencies = ["flask>=2.0"]

[dependency-groups.dev]
pytest = ">=7.0"
`)

	ds, err := Resolve(dir)
	require.NoError(t, err)

	flask := find(ds, "flask")
	require.NotNil(t, flask)
	assert.Equal(t, "2.3.0", flask.Version)
	assert.Equal(t, EcosystemPyPI, flask.Ecosystem)
	assert.Equal(t, KindProd, flask.Kind)

	pytest := find(ds, "pytest")
	require.NotNil(t, pytest)
	assert.Equal(t, "7.4.0", pytest.Version)
	assert.Equal(t, KindDev, pytest.Kind, "pytest is in [dependency-groups.dev]")

	httpx := find(ds, "httpx")
	require.NotNil(t, httpx)
	assert.Equal(t, KindProd, httpx.Kind, "httpx not in any dev group → prod")
}

func TestResolvePython_UVLock_UVDevDependencies(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "uv.lock", `version = 1

[[package]]
name = "black"
version = "24.0.0"
`)
	writeFile(t, dir, "pyproject.toml", `[project]
name = "myapp"

[tool.uv.dev-dependencies]
black = ">=24.0"
`)

	ds, err := Resolve(dir)
	require.NoError(t, err)

	black := find(ds, "black")
	require.NotNil(t, black)
	assert.Equal(t, KindDev, black.Kind, "black is in [tool.uv.dev-dependencies]")
}

func TestResolvePython_NormalizesNames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", `
SQLAlchemy==2.0.0
Pillow==10.0.0
typing_extensions==4.8.0
`)
	ds, err := Resolve(dir)
	require.NoError(t, err)

	assert.NotNil(t, find(ds, "sqlalchemy"), "SQLAlchemy → sqlalchemy")
	assert.NotNil(t, find(ds, "pillow"), "Pillow → pillow")
	assert.NotNil(t, find(ds, "typing-extensions"), "typing_extensions → typing-extensions")
}

func TestResolvePython_EmptyDir(t *testing.T) {
	ds, err := Resolve(t.TempDir())
	require.NoError(t, err)
	for _, d := range ds {
		assert.NotEqual(t, EcosystemPyPI, d.Ecosystem, "no Python manifests → no pypi deps")
	}
}

// findByEcosystem finds a dep by ecosystem and name substring (for negative checks).
func findByEcosystem(ds []Dependency, eco, name string) *Dependency {
	for i := range ds {
		if ds[i].Ecosystem == eco && ds[i].Name == name {
			return &ds[i]
		}
	}
	return nil
}
