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

func TestRCOrdering(t *testing.T) {
	ordered := []string{"1.3.6", "1.4.0-rc.1", "1.4.0-rc.2", "1.4.0-rc.9", "1.4.0-rc.10", "1.4.0-rc.11", "1.4.0", "1.4.1-rc.1", "1.4.1"}
	for index := 0; index < len(ordered)-1; index++ {
		for next := index + 1; next < len(ordered); next++ {
			left, _ := parseVersion(ordered[index])
			right, _ := parseVersion(ordered[next])
			if compareVersions(left, right) >= 0 || compareVersions(right, left) <= 0 {
				t.Fatalf("%s must sort before %s", ordered[index], ordered[next])
			}
		}
	}
}

func TestReleaseChannelParsing(t *testing.T) {
	for _, value := range []string{"stable", "rc"} {
		if channel, ok := ParseReleaseChannel(value); !ok || string(channel) != value {
			t.Fatalf("ParseReleaseChannel(%q) = %q, %v", value, channel, ok)
		}
	}
	for _, value := range []string{"", "beta", "RC", "Stable", " rc"} {
		if _, ok := ParseReleaseChannel(value); ok {
			t.Fatalf("ParseReleaseChannel(%q) must fail", value)
		}
		if NormalizeReleaseChannel(value) != ChannelStable {
			t.Fatalf("NormalizeReleaseChannel(%q) must fall back to stable", value)
		}
	}
	if NormalizeReleaseChannel("rc") != ChannelRC {
		t.Fatal("rc must stay rc")
	}
}

func TestIsRCPrerelease(t *testing.T) {
	for pre, want := range map[string]bool{
		"rc.1": true, "rc.10": true, "rc.0": true,
		"rc": false, "rc.": false, "rc.01": false, "rc.1.1": false, "rc.a": false, "rc.1-x": false,
		"beta.1": false, "alpha": false, "": false, "RC.1": false,
	} {
		if got := isRCPrerelease(pre); got != want {
			t.Fatalf("isRCPrerelease(%q) = %v, want %v", pre, got, want)
		}
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
