package store

import "testing"

func TestAssetEnvironment(t *testing.T) {
	for _, test := range []struct {
		name string
		tags string
		want string
	}{
		{name: "unmarked", tags: "linux, europe", want: ""},
		{name: "existing production tag", tags: "linux, Production", want: "production"},
		{name: "production alias", tags: "prod", want: "production"},
		{name: "explicit environment tag", tags: "env:production", want: "production"},
		{name: "environment tag normalization", tags: " env: PROD ", want: "production"},
		{name: "staging", tags: "staging", want: "staging"},
		{name: "explicit custom environment", tags: "env:qa", want: "qa"},
		{name: "production wins conflicting markers", tags: "env:staging, production", want: "production"},
		{name: "similar ordinary tag is not an environment", tags: "production-like", want: ""},
		{name: "empty environment is unmarked", tags: "env:", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := AssetEnvironment(AssetRow{Tags: test.tags}); got != test.want {
				t.Fatalf("AssetEnvironment(%q) = %q, want %q", test.tags, got, test.want)
			}
		})
	}
}
