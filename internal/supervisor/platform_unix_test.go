//go:build unix

package supervisor

import "testing"

func TestMergePATHKeepsBaseFirstDedupesAndDropsRelative(t *testing.T) {
	got := mergePATH("/usr/local/bin:/usr/bin:rel/bin:/usr/bin", "/opt/homebrew/bin:/usr/bin:./x::/usr/local/bin:/home/u/.cargo/bin")
	want := "/usr/local/bin:/usr/bin:/opt/homebrew/bin:/home/u/.cargo/bin"
	if got != want {
		t.Fatalf("mergePATH = %q, want %q", got, want)
	}
	if got := mergePATH("", "/a:/b"); got != "/a:/b" {
		t.Fatalf("empty base = %q", got)
	}
	if got := mergePATH("/a", ""); got != "/a" {
		t.Fatalf("empty extra = %q", got)
	}
}
