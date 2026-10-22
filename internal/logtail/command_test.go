package logtail

import (
	"errors"
	"strings"
	"testing"
)

func TestSourceCommandQuotesReadOnlyArguments(t *testing.T) {
	for _, test := range []struct {
		name   string
		source Source
		want   string
	}{
		{
			name:   "file with shell metacharacters and quote",
			source: Source{Kind: SourceFile, Value: `/tmp/a'b;$(touch /tmp/pwn)`},
			want:   `tail -n 200 -f -- '/tmp/a'"'"'b;$(touch /tmp/pwn)'`,
		},
		{
			name:   "journal unit cannot add flags",
			source: Source{Kind: SourceJournal, Value: `nginx.service; reboot`},
			want:   `journalctl -u 'nginx.service; reboot' -f --no-pager`,
		},
		{
			name:   "docker container cannot add command",
			source: Source{Kind: SourceDocker, Value: `web$(id)`},
			want:   `docker logs -f --tail 200 -- 'web$(id)'`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := sourceCommand(test.source)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("command = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSourceCommandRejectsInvalidInput(t *testing.T) {
	for _, value := range []string{"", "  ", "line\nbreak", "line\rbreak", "nul\x00byte", strings.Repeat("x", MaxSourceValueSize+1)} {
		if _, err := sourceCommand(Source{Kind: SourceFile, Value: value}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("value %q error = %v, want ErrInvalid", value, err)
		}
	}
	if _, err := sourceCommand(Source{Kind: "process", Value: "nginx"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown source error = %v, want ErrInvalid", err)
	}
}
