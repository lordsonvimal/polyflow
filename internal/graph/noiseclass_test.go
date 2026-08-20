package graph

import "testing"

func TestClassifyEdgeNoise(t *testing.T) {
	plainDst := &Node{ID: "n1", Type: NodeTypeFunction}
	elementDst := &Node{ID: "n2", Type: NodeTypeElement}

	tests := []struct {
		name string
		e    *Edge
		dst  *Node
		want NoiseClass
	}{
		{
			name: "rails_filter tagged calls edge",
			e:    &Edge{Type: EdgeTypeCalls, Meta: map[string]string{"via": "rails_filter"}},
			dst:  plainDst,
			want: NoiseFilterChain,
		},
		{
			name: "gin_middleware_use tagged calls edge",
			e:    &Edge{Type: EdgeTypeCalls, Meta: map[string]string{"via": "gin_middleware_use"}},
			dst:  plainDst,
			want: NoiseFilterChain,
		},
		{
			name: "express_middleware_use tagged calls edge",
			e:    &Edge{Type: EdgeTypeCalls, Meta: map[string]string{"via": "express_middleware_use"}},
			dst:  plainDst,
			want: NoiseFilterChain,
		},
		{
			name: "inherits edge",
			e:    &Edge{Type: EdgeTypeInherits},
			dst:  plainDst,
			want: NoiseMixin,
		},
		{
			name: "contains edge",
			e:    &Edge{Type: EdgeTypeContains},
			dst:  plainDst,
			want: NoiseContainment,
		},
		{
			name: "edge into element node",
			e:    &Edge{Type: EdgeTypeCalls},
			dst:  elementDst,
			want: NoiseRenderTree,
		},
		{
			name: "plain calls edge",
			e:    &Edge{Type: EdgeTypeCalls},
			dst:  plainDst,
			want: NoiseNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyEdgeNoise(tt.e, tt.dst)
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
			want: NoiseInclude{NoiseFilterChain: true, NoiseMixin: true, NoiseContainment: true, NoiseRenderTree: true},
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
