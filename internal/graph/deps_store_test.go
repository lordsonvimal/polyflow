package graph

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDependenciesTable(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	require.NoError(t, store.UpsertDependency(ctx, &Dependency{
		Service: "dsw-agent", Ecosystem: "go", Name: "github.com/aws/aws-sdk-go",
		Version: "v1.55.8", Kind: "prod",
	}))
	require.NoError(t, store.UpsertDependency(ctx, &Dependency{
		Service: "dsw-manager", Ecosystem: "go", Name: "github.com/aws/aws-sdk-go-v2/service/s3",
		Version: "v1.66.0", Kind: "prod",
	}))
	require.NoError(t, store.UpsertDependency(ctx, &Dependency{
		Service: "mdr", Ecosystem: "npm", Name: "jquery", Version: "3.7.1", Kind: "dev",
	}))

	// "What version of aws-sdk-go does dsw-agent use?"
	agent, err := store.ListDependencies(ctx, "dsw-agent")
	require.NoError(t, err)
	require.Len(t, agent, 1)
	assert.Equal(t, "v1.55.8", agent[0].Version)

	all, err := store.ListDependencies(ctx, "")
	require.NoError(t, err)
	assert.Len(t, all, 3)

	// Upsert replaces the version for the same (service, ecosystem, name).
	require.NoError(t, store.UpsertDependency(ctx, &Dependency{
		Service: "dsw-agent", Ecosystem: "go", Name: "github.com/aws/aws-sdk-go",
		Version: "v1.56.0", Kind: "prod",
	}))
	agent, err = store.ListDependencies(ctx, "dsw-agent")
	require.NoError(t, err)
	require.Len(t, agent, 1)
	assert.Equal(t, "v1.56.0", agent[0].Version)
}
