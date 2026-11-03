package supervisor

import (
	"errors"
	"strings"
	"testing"
)

func TestUntrustedAdviceNeverSuggestsDeletingStateDir(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		text := untrustedAdvice(goos, EndpointStateDir, "/data/state", 0)
		if !strings.Contains(text, "/data/state") || strings.Contains(text, "rm ") {
			t.Errorf("%s state dir advice %q must name the path and not suggest rm", goos, text)
		}
	}
	if text := untrustedAdvice("linux", EndpointStateDir, "/data/state", 0); !strings.Contains(text, "chown") {
		t.Errorf("unix state dir advice %q should suggest chown", text)
	}
	if text := untrustedAdvice("linux", EndpointSocketDir, "/tmp/sock-dir", 7); !strings.Contains(text, "rm ") || !strings.Contains(text, "/tmp/sock-dir") {
		t.Errorf("socket dir advice %q should suggest rm of the directory", text)
	}
	if text := untrustedAdvice("linux", EndpointSocket, "/tmp/sock-dir/a.sock", 7); !strings.Contains(text, "rm ") || !strings.Contains(text, "'/tmp/sock-dir'") {
		t.Errorf("socket advice %q should suggest rm of its directory", text)
	}
}

func TestUntrustedAdviceOnWindowsHasNoUnixCommands(t *testing.T) {
	for _, kind := range []EndpointKind{EndpointStateDir, EndpointSocketDir, EndpointSocket, EndpointPipe, EndpointElevated} {
		text := untrustedAdvice("windows", kind, `C:\data\state`, -1)
		for _, banned := range []string{"sudo", "rm ", "chown"} {
			if strings.Contains(text, banned) {
				t.Errorf("windows advice for kind %d contains %q: %s", kind, banned, text)
			}
		}
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	if got := shellQuote("/tmp/a'b"); got != `'/tmp/a'\''b'` {
		t.Fatalf("shellQuote = %s", got)
	}
	if text := untrustedAdvice("linux", EndpointSocketDir, "/tmp/a'b", 1); !strings.Contains(text, `'/tmp/a'\''b'`) {
		t.Fatalf("advice %q does not quote the path", text)
	}
}

func TestUntrustedEndpointKindsStayInvalidInput(t *testing.T) {
	for _, kind := range []EndpointKind{EndpointStateDir, EndpointSocketDir} {
		err := error(&UntrustedEndpointError{Kind: kind, Path: "/x", OwnerUID: 1})
		if !errors.Is(err, ErrUntrustedEndpoint) || !errors.Is(err, ErrInvalidInput) {
			t.Errorf("kind %d: %v", kind, err)
		}
	}
}
