package graph

import "testing"

func TestClassifyEdgeNoise(t *testing.T) {
	plainSrc := &Node{ID: "n0", Type: NodeTypeFunction, File: "internal/foo/foo.go"}
	plainDst := &Node{ID: "n1", Type: NodeTypeFunction, File: "internal/foo/bar.go"}
	elementDst := &Node{ID: "n2", Type: NodeTypeElement}
	testSrc := &Node{ID: "n3", Type: NodeTypeFunction, File: "internal/foo/foo_test.go"}
	testDst := &Node{ID: "n4", Type: NodeTypeFunction, File: "web/src/components/Foo.test.tsx"}

	tests := []struct {
		name string
		e    *Edge
		src  *Node
		dst  *Node
		want NoiseClass
	}{
		{
			name: "rails_filter tagged calls edge",
			e:    &Edge{Type: EdgeTypeCalls, Meta: map[string]string{"via": "rails_filter"}},
			src:  plainSrc,
			dst:  plainDst,
			want: NoiseFilterChain,
		},
		{
			name: "gin_middleware_use tagged calls edge",
			e:    &Edge{Type: EdgeTypeCalls, Meta: map[string]string{"via": "gin_middleware_use"}},
			src:  plainSrc,
			dst:  plainDst,
			want: NoiseFilterChain,
		},
		{
			name: "express_middleware_use tagged calls edge",
			e:    &Edge{Type: EdgeTypeCalls, Meta: map[string]string{"via": "express_middleware_use"}},
			src:  plainSrc,
			dst:  plainDst,
			want: NoiseFilterChain,
		},
		{
			name: "inherits edge",
			e:    &Edge{Type: EdgeTypeInherits},
			src:  plainSrc,
			dst:  plainDst,
			want: NoiseMixin,
		},
		{
			name: "contains edge",
			e:    &Edge{Type: EdgeTypeContains},
			src:  plainSrc,
			dst:  plainDst,
			want: NoiseContainment,
		},
		{
			name: "edge into element node",
			e:    &Edge{Type: EdgeTypeCalls},
			src:  plainSrc,
			dst:  elementDst,
			want: NoiseRenderTree,
		},
		{
			name: "plain calls edge",
			e:    &Edge{Type: EdgeTypeCalls},
			src:  plainSrc,
			dst:  plainDst,
			want: NoiseNone,
		},
		{
			name: "calls edge from a Go test file",
			e:    &Edge{Type: EdgeTypeCalls},
			src:  testSrc,
			dst:  plainDst,
			want: NoiseTestCode,
		},
		{
			name: "calls edge into a JS/TS test file",
			e:    &Edge{Type: EdgeTypeCalls},
			src:  plainSrc,
			dst:  testDst,
			want: NoiseTestCode,
		},
		{
			name: "calls edge with nil src",
			e:    &Edge{Type: EdgeTypeCalls},
			src:  nil,
			dst:  testDst,
			want: NoiseTestCode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyEdgeNoise(tt.e, tt.src, tt.dst)
			if got != tt.want {
				t.Errorf("ClassifyEdgeNoise() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseNoiseInclude(t *testing.T) {
	tests := []struct {
		name    string
		keys    []string
		want    NoiseInclude
		wantErr bool
	}{
		{
			name: "all",
			keys: []string{"all"},
			want: NoiseInclude{NoiseFilterChain: true, NoiseMixin: true, NoiseContainment: true, NoiseRenderTree: true, NoiseTestCode: true},
		},
		{
			name: "none",
			keys: []string{"none"},
			want: NoiseInclude{},
		},
		{
			name: "empty",
			keys: nil,
			want: NoiseInclude{},
		},
		{
			name: "one class",
			keys: []string{"render_tree"},
			want: NoiseInclude{NoiseRenderTree: true},
		},
		{
			name:    "unknown class",
			keys:    []string{"bogus"},
			wantErr: true,
		},
		{
			name:    "mixed valid and invalid",
			keys:    []string{"render_tree", "bogus"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseNoiseInclude(tt.keys)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseNoiseInclude(%v) expected error, got nil", tt.keys)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseNoiseInclude(%v) unexpected error: %v", tt.keys, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParseNoiseInclude(%v) = %v, want %v", tt.keys, got, tt.want)
			}
			for c, v := range tt.want {
				if got[c] != v {
					t.Errorf("ParseNoiseInclude(%v)[%q] = %v, want %v", tt.keys, c, got[c], v)
				}
			}
		})
	}
}

func TestNoiseIncludeAllows(t *testing.T) {
	n := NoiseInclude{NoiseRenderTree: true}
	if !n.Allows(NoiseNone) {
		t.Error("Allows(NoiseNone) should always be true")
	}
	if !n.Allows(NoiseRenderTree) {
		t.Error("Allows(NoiseRenderTree) should be true when included")
	}
	if n.Allows(NoiseMixin) {
		t.Error("Allows(NoiseMixin) should be false when not included")
	}
}

func TestDefaultNoiseInclude(t *testing.T) {
	gen := DefaultNoiseInclude("generate")
	if !gen[NoiseRenderTree] || len(gen) != 1 {
		t.Errorf("DefaultNoiseInclude(generate) = %v, want only render_tree", gen)
	}

	for _, intent := range []string{"impact", "debug", "refactor", ""} {
		got := DefaultNoiseInclude(intent)
		if len(got) != 0 {
			t.Errorf("DefaultNoiseInclude(%q) = %v, want empty", intent, got)
		}
	}
}
