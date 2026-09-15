package factpipe

// FX.8.32 (2026-09-15): file_routes — Tier FX migration of
// internal/linker/file_routes.go's SynthesizeFileRoutes (Phase M.0). These
// port the retired Go test's pure path-mapping/classification unit tests
// verbatim (fr-prefixed), same-package (white-box) since those helpers stay
// unexported. See internal/factpipe/pipeline/file_routes_test.go for the
// full hub-through-pipeline.Run integration coverage the retired test
// suite never had.

import "testing"

func TestFRNextPagesPath(t *testing.T) {
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
		got, ok := frNextPagesPath(c.in)
		if ok != c.ok {
			t.Errorf("ok for %q = %v, want %v", c.in, ok, c.ok)
		}
		if c.ok && got != c.want {
			t.Errorf("path for %q = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFRNextSegmentPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "/", true},
		{"dashboard", "/dashboard", true},
		{"(marketing)/pricing", "/pricing", true},
		{"api/users/[id]", "/api/users/:id", true},
		{"@modal/inbox", "", false},
		{"[[...opt]]", "", false},
		{"[...slug]", "/*", true},
	}
	for _, c := range cases {
		got, ok := frNextSegmentPath(c.in, true)
		if ok != c.ok {
			t.Errorf("ok for %q = %v, want %v", c.in, ok, c.ok)
		}
		if c.ok && got != c.want {
			t.Errorf("path for %q = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFRNuxtServerPath(t *testing.T) {
	cases := []struct {
		in     string
		path   string
		method string
		ok     bool
	}{
		{"items.get.ts", "/api/items", "GET", true},
		{"items.post.ts", "/api/items", "POST", true},
		{"items.ts", "/api/items", "", true},
		{"users/[id].get.ts", "/api/users/:id", "GET", true},
		{"[...slug].ts", "/api/*", "", true},
	}
	for _, c := range cases {
		rp, m, ok := frNuxtServerPath(c.in)
		if ok != c.ok {
			t.Errorf("ok for %q = %v, want %v", c.in, ok, c.ok)
		}
		if c.ok {
			if rp != c.path {
				t.Errorf("path for %q = %q, want %q", c.in, rp, c.path)
			}
			if m != c.method {
				t.Errorf("method for %q = %q, want %q", c.in, m, c.method)
			}
		}
	}
}

func TestFRRemixPath(t *testing.T) {
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
		got, ok := frRemixPath(c.in)
		if ok != c.ok {
			t.Errorf("ok for %q = %v, want %v", c.in, ok, c.ok)
		}
		if c.ok && got != c.want {
			t.Errorf("path for %q = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFRIsPageFile(t *testing.T) {
	cases := []struct {
		framework string
		file      string
		want      bool
	}{
		{"next-pages", "about.tsx", true},
		{"next-pages", "api/users.ts", false},
		{"next-pages", "_app.tsx", false},
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
		if got := frIsPageFile(c.file, c.framework); got != c.want {
			t.Errorf("frIsPageFile(%q, %q) = %v, want %v", c.file, c.framework, got, c.want)
		}
	}
}

func TestFRIsHandlerFile(t *testing.T) {
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
		if got := frIsHandlerFile(c.file, c.framework); got != c.want {
			t.Errorf("frIsHandlerFile(%q, %q) = %v, want %v", c.file, c.framework, got, c.want)
		}
	}
}
