package shellintegr

import (
	"os"
	"slices"
	"testing"
)

func TestBashBannerEnv(t *testing.T) {
	if err := os.Unsetenv(bashSilenceKey); err != nil {
		t.Fatal(err)
	}
	t.Setenv(bashSilenceKey, "")
	if err := os.Unsetenv(bashSilenceKey); err != nil {
		t.Fatal(err)
	}
	want := []string{bashSilenceKey + "=1"}
	if got := BashBannerEnv("darwin", "/bin/bash", nil); !slices.Equal(got, want) {
		t.Fatalf("darwin bash = %v, want %v", got, want)
	}
	if got := BashBannerEnv("linux", "/bin/bash", nil); got != nil {
		t.Fatalf("linux bash = %v, want nil", got)
	}
	if got := BashBannerEnv("darwin", "/bin/zsh", nil); got != nil {
		t.Fatalf("darwin zsh = %v, want nil", got)
	}
	if got := BashBannerEnv("darwin", "/bin/bash", []string{bashSilenceKey + "=0"}); got != nil {
		t.Fatalf("user-set in env = %v, want nil", got)
	}
	t.Setenv(bashSilenceKey, "0")
	if got := BashBannerEnv("darwin", "/bin/bash", nil); got != nil {
		t.Fatalf("user-set in process = %v, want nil", got)
	}
}
