package deps

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestDatastoreNodes_DualSQLiteDrivers(t *testing.T) {
	// Both SQLite drivers (pure-Go modernc and cgo mattn) must map to ONE
	// logical sqlite node with driver metadata distinguishing them.
	nodes := DatastoreNodes("chessleap", []Dependency{
		{Ecosystem: EcosystemGo, Name: "modernc.org/sqlite", Version: "v1.37.1"},
		{Ecosystem: EcosystemGo, Name: "github.com/mattn/go-sqlite3", Version: "v1.14.22"},
		{Ecosystem: EcosystemGo, Name: "github.com/gin-gonic/gin", Version: "v1.10.0"},
	})
	require.Len(t, nodes, 1)
	n := nodes[0]
	assert.Equal(t, graph.NodeTypeDatastore, n.Type)
	assert.Equal(t, "sqlite", n.Meta["engine"])
	assert.Contains(t, n.Meta["driver"], "modernc (pure Go)")
	assert.Contains(t, n.Meta["driver"], "mattn (cgo)")
	assert.Equal(t, "store", n.Meta["kind"])
}

func TestDatastoreNodes_PostgresVariants(t *testing.T) {
	// lib/pq (synergy/core) and GORM's postgres dialector (mysycamore,
	// dsw-manager) both resolve to a postgres store; GORM adds orm metadata.
	libpq := DatastoreNodes("core", []Dependency{
		{Ecosystem: EcosystemGo, Name: "github.com/lib/pq", Version: "v1.10.9"},
	})
	require.Len(t, libpq, 1)
	assert.Equal(t, "postgres", libpq[0].Meta["engine"])
	assert.Empty(t, libpq[0].Meta["orm"])

	gormPg := DatastoreNodes("mysycamore", []Dependency{
		{Ecosystem: EcosystemGo, Name: "gorm.io/driver/postgres", Version: "v1.5.9"},
	})
	require.Len(t, gormPg, 1)
	assert.Equal(t, "postgres", gormPg[0].Meta["engine"])
	assert.Equal(t, "gorm", gormPg[0].Meta["orm"])
}

func TestDatastoreNodes_MultiEngine(t *testing.T) {
	// dsw-manager: dual Postgres/SQLite via GORM → two logical stores.
	nodes := DatastoreNodes("dsw-manager", []Dependency{
		{Ecosystem: EcosystemGo, Name: "gorm.io/driver/postgres", Version: "v1.5.9"},
		{Ecosystem: EcosystemGo, Name: "gorm.io/driver/sqlite", Version: "v1.5.6"},
	})
	require.Len(t, nodes, 2)
	engines := []string{nodes[0].Meta["engine"], nodes[1].Meta["engine"]}
	assert.ElementsMatch(t, []string{"postgres", "sqlite"}, engines)
}

func TestDatastoreNodes_NoDrivers(t *testing.T) {
	assert.Empty(t, DatastoreNodes("wopi-host", []Dependency{
		{Ecosystem: EcosystemGo, Name: "github.com/gin-gonic/gin", Version: "v1.10.0"},
	}))
}
