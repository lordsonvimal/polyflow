package linker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNextPagesPath verifies the next-pages / nuxt path-mapping dialect.
func TestNextPagesPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"about.tsx", "/about", true},
		{"index.tsx", "/", true},
		{"posts/[id].tsx", "/posts/:id", true},
		{"[...slug].tsx", "/*", true},
		{"blog/index.tsx", "/blog", true},
	}
	for _, c := range cases {
		got, ok := nextPagesPath(c.in)
		assert.Equal(t, c.ok, ok, "ok for %q", c.in)
		if c.ok {
			assert.Equal(t, c.want, got, "path for %q", c.in)
		}
	}
}

// TestNextSegmentPath verifies route-group stripping and ledger triggers.
func TestNextSegmentPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "/", true},
		{"dashboard", "/dashboard", true},
		{"(marketing)/pricing", "/pricing", true},
		{"api/users/[id]", "/api/users/:id", true},
		// parallel route → ledger
		{"@modal/inbox", "", false},
		// optional catch-all → ledger
		{"[[...opt]]", "", false},
		// catch-all → wildcard
		{"[...slug]", "/*", true},
	}
	for _, c := range cases {
		got, ok := nextSegmentPath(c.in, true)
		assert.Equal(t, c.ok, ok, "ok for %q", c.in)
		if c.ok {
			assert.Equal(t, c.want, got, "path for %q", c.in)
		}
	}
}

// TestNuxtServerPath verifies method extraction from filename suffix.
func TestNuxtServerPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		path   string
		method string
		ok     bool
	}{
		{"items.get.ts", "/api/items", "GET", true},
		{"items.post.ts", "/api/items", "POST", true},
		{"items.ts", "/api/items", "", true},   // no suffix → ALL (method="")
		{"users/[id].get.ts", "/api/users/:id", "GET", true},
		{"[...slug].ts", "/api/*", "", true},
	}
	for _, c := range cases {
		rp, m, ok := nuxtServerPath(c.in)
		assert.Equal(t, c.ok, ok, "ok for %q", c.in)
		if c.ok {
			assert.Equal(t, c.path, rp, "path for %q", c.in)
			assert.Equal(t, c.method, m, "method for %q", c.in)
		}
	}
}

// TestRemixPath verifies the Remix dot-separator and $param conventions.
func TestRemixPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"_index.tsx", "/", true},
		{"posts.$postId.tsx", "/posts/:postId", true},
		{"about.tsx", "/about", true},
		{"settings.profile.tsx", "/settings/profile", true},
	}
	for _, c := range cases {
		got, ok := remixPath(c.in)
		assert.Equal(t, c.ok, ok, "ok for %q", c.in)
		if c.ok {
			assert.Equal(t, c.want, got, "path for %q", c.in)
		}
	}
}

// TestIsPageFile and TestIsHandlerFile verify per-framework file classification.
func TestIsPageFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		framework string
		file      string
		want      bool
	}{
		{"next-pages", "about.tsx", true},
		{"next-pages", "api/users.ts", false},   // api → handler, not page
		{"next-pages", "_app.tsx", false},        // _ prefix → skip
		{"next-app", "dashboard/page.tsx", true},
		{"next-app", "dashboard/route.ts", false},
		{"next-app", "dashboard/layout.tsx", false},
		{"sveltekit", "blog/[slug]/+page.svelte", true},
		{"sveltekit", "api/items/+server.ts", false},
		{"nuxt", "users/[id].vue", true},
		{"nuxt-server", "items.get.ts", false},
		{"remix", "_index.tsx", true},
	}
	for _, c := range cases {
		got := isPageFile(c.file, c.framework)
		assert.Equal(t, c.want, got, "isPageFile(%q, %q)", c.file, c.framework)
	}
}

func TestIsHandlerFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		framework string
		file      string
		want      bool
	}{
		{"next-pages", "api/users/[id].ts", true},
		{"next-pages", "about.tsx", false},
		{"next-app", "api/users/route.ts", true},
		{"next-app", "api/users/page.tsx", false},
		{"sveltekit", "api/items/+server.ts", true},
		{"sveltekit", "blog/[slug]/+page.svelte", false},
		{"nuxt-server", "items.get.ts", true},
		{"nuxt", "pages/users.vue", false},
	}
	for _, c := range cases {
		got := isHandlerFile(c.file, c.framework)
		assert.Equal(t, c.want, got, "isHandlerFile(%q, %q)", c.file, c.framework)
	}
}
