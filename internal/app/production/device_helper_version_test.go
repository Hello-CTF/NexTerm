package production

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func TestDeviceHelperReportsAppVersionAsLocalSide(t *testing.T) {
	running := supervisor.RuntimeMetadata{Version: "1.4.3", Commit: "abc1234", Protocol: supervisor.ProtocolVersion, SHA256: "ff"}
	result := deviceHelperDTO("s1", "status", running, true)
	if !result.Local.Known || result.Local.Protocol == nil || *result.Local.Protocol != supervisor.ProtocolVersion {
		t.Fatalf("local side = %+v, want this app's release and protocol", result.Local)
	}
	if result.Local.SHA256 != "" {
		t.Fatalf("local side carries SHA-256 %q, but the device runs its own agent binary", result.Local.SHA256)
	}
	if !result.Running.Known || result.Running.Release != "1.4.3" || result.Compatibility != "compatible" {
		t.Fatalf("running=%+v compatibility=%q", result.Running, result.Compatibility)
	}
	if unknown := deviceHelperDTO("s1", "status", supervisor.RuntimeMetadata{}, false); unknown.Running.Known || unknown.Compatibility != "unknown" || !unknown.Local.Known {
		t.Fatalf("unknown running helper = %+v", unknown)
	}
}

func TestAppHelperVersionOmitsPlaceholderCommit(t *testing.T) {
	if got := appHelperVersion("dev", "unknown"); got.Commit != "" || got.Release != "dev" {
		t.Fatalf("dev build = %+v", got)
	}
	if got := appHelperVersion("1.4.3", "abc1234"); got.Commit != "abc1234" {
		t.Fatalf("release build = %+v", got)
	}
}
