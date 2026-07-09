package deps

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// driverInfo maps a dependency name to the logical datastore it implies.
type driverInfo struct {
	engine string // sqlite | postgres | mysql
	driver string // human-readable driver identity
	orm    string // "" for raw drivers
}

// knownDatastoreDrivers: different driver packages for the same engine map to
// ONE logical datastore node with driver metadata — modernc (pure Go) and
// mattn (cgo) SQLite are the same store, not two unrelated things.
var knownDatastoreDrivers = map[string]driverInfo{
	"modernc.org/sqlite":          {engine: "sqlite", driver: "modernc (pure Go)"},
	"github.com/mattn/go-sqlite3": {engine: "sqlite", driver: "mattn (cgo)"},
	"github.com/lib/pq":           {engine: "postgres", driver: "lib/pq"},
	"github.com/jackc/pgx":        {engine: "postgres", driver: "pgx"},
	"github.com/jackc/pgx/v4":     {engine: "postgres", driver: "pgx"},
	"github.com/jackc/pgx/v5":     {engine: "postgres", driver: "pgx"},
	"gorm.io/driver/postgres":     {engine: "postgres", driver: "gorm/postgres", orm: "gorm"},
	"gorm.io/driver/sqlite":       {engine: "sqlite", driver: "gorm/sqlite", orm: "gorm"},
	"github.com/go-sql-driver/mysql": {engine: "mysql", driver: "go-sql-driver"},
	// Ruby
	"pg":      {engine: "postgres", driver: "pg gem"},
	"sqlite3": {engine: "sqlite", driver: "sqlite3 gem"},
	"mysql2":  {engine: "mysql", driver: "mysql2 gem"},
}

// DatastoreNodes derives service-level datastore nodes from resolved
// dependencies. One node per (service, engine); multiple drivers for the same
// engine merge into that node's driver metadata.
func DatastoreNodes(service string, ds []Dependency) []graph.Node {
	type agg struct {
		drivers  []string
		versions []string
		orm      string
	}
	engines := map[string]*agg{}
	for _, d := range ds {
		info, ok := knownDatastoreDrivers[d.Name]
		if !ok {
			continue
		}
		a := engines[info.engine]
		if a == nil {
			a = &agg{}
			engines[info.engine] = a
		}
		a.drivers = append(a.drivers, info.driver)
		a.versions = append(a.versions, d.Name+"@"+d.Version)
		if info.orm != "" {
			a.orm = info.orm
		}
	}

	var out []graph.Node
	for engine, a := range engines {
		sort.Strings(a.drivers)
		sort.Strings(a.versions)
		meta := map[string]string{
			"kind":     "store",
			"engine":   engine,
			"driver":   strings.Join(a.drivers, ", "),
			"packages": strings.Join(a.versions, ", "),
		}
		if a.orm != "" {
			meta["orm"] = a.orm
		}
		out = append(out, graph.Node{
			ID:      fmt.Sprintf("%s:datastore:%s", service, engine),
			Type:    graph.NodeTypeDatastore,
			Label:   engine,
			Service: service,
			Meta:    meta,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
