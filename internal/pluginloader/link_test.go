package pluginloader

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/deps"
)

func TestVersionQualifies(t *testing.T) {
	svcDeps := []deps.Dependency{
		{Ecosystem: deps.EcosystemGo, Name: "github.com/gofiber/fiber/v2", Version: "v2.60.0"},
	}

	cases := []struct {
		name    string
		c       Component
		wantOK  bool
		wantVer string
	}{
		{
			name:    "no version_range always qualifies",
			c:       Component{Package: "github.com/gofiber/fiber/v2"},
			wantOK:  true,
			wantVer: "v2.60.0",
		},
		{
			name:    "in range qualifies",
			c:       Component{Package: "github.com/gofiber/fiber/v2", VersionRange: ">=2.0.0,<3.0.0"},
			wantOK:  true,
			wantVer: "v2.60.0",
		},
		{
			name:    "out of range does not qualify",
			c:       Component{Package: "github.com/gofiber/fiber/v2", VersionRange: ">=2.90.0,<3.0.0"},
			wantOK:  false,
			wantVer: "v2.60.0",
		},
		{
			name:    "unresolved package with version_range does not qualify",
			c:       Component{Package: "github.com/other/pkg", VersionRange: ">=1.0.0"},
			wantOK:  false,
			wantVer: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, ver := VersionQualifies(tc.c, svcDeps)
			if ok != tc.wantOK || ver != tc.wantVer {
				t.Errorf("VersionQualifies() = (%v, %q), want (%v, %q)", ok, ver, tc.wantOK, tc.wantVer)
			}
		})
	}
}
