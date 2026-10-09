package update

import "testing"

func TestParseAndCompareVersions(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"0.2.2", "0.2.2", 0},
		{"v0.2.2", "0.2.2", 0},
		{"0.2.3", "0.2.2", 1},
		{"0.2.2", "0.3.0", -1},
		{"1.0.0", "0.99.99", 1},
		{"0.2.2-rc.1", "0.2.2", -1},
		{"0.2.2", "0.2.2-rc.1", 1},
		{"0.2.2-rc.1", "0.2.2-rc.2", -1},
		{"0.2.2-rc.2", "0.2.2-rc.1", 1},
		{"0.2.2-rc.1", "0.2.2-beta.1", 1},
		{"0.2.2-rc.1", "0.2.2-rc.1.1", -1},
		{"0.2", "0.2.0", 0},
	}
	for _, test := range cases {
		left, leftOK := parseVersion(test.left)
		right, rightOK := parseVersion(test.right)
		if !leftOK || !rightOK {
			t.Fatalf("parseVersion(%q, %q) failed", test.left, test.right)
		}
		if got := compareVersions(left, right); got != test.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
	for _, invalid := range []string{"", "dev", "0.2.x", "0.2.2.2.2", "v"} {
		if _, ok := parseVersion(invalid); ok {
			t.Fatalf("parseVersion(%q) must fail", invalid)
		}
	}
}

func TestAcceptsPrereleaseFollowsCurrentChannel(t *testing.T) {
	if acceptsPrerelease("0.2.2") {
		t.Fatal("stable current must not accept prerelease")
	}
	if !acceptsPrerelease("0.2.2-rc.7") {
		t.Fatal("rc current must accept prerelease")
	}
	if !acceptsPrerelease("dev") {
		t.Fatal("dev current must accept prerelease")
	}
}

func TestIsNewerVersion(t *testing.T) {
	if !isNewerVersion("0.3.0", "0.2.2") {
		t.Fatal("0.3.0 must be newer than 0.2.2")
	}
	if isNewerVersion("0.2.2", "0.2.2") {
		t.Fatal("equal versions must not be newer")
	}
	if !isNewerVersion("0.2.2", "dev") {
		t.Fatal("any release must be newer than unparseable current")
	}
	if isNewerVersion("not-a-version", "0.2.2") {
		t.Fatal("unparseable candidate must not be newer")
	}
}
