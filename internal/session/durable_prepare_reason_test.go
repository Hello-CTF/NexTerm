package session

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// An older helper that still owns the state directory is reported as a message the user can act on, not as the raw
// protocol error chain.
func TestDurableReadinessReasonExplainsLegacyHelper(t *testing.T) {
	err := fmt.Errorf("%w: running supervisor daemon is incompatible or owns different state; refusing to replace it: %w",
		durable.ErrUnavailable, supervisor.ErrLegacyHelperRunning)
	reason := durableReadinessReason(err)
	if strings.Contains(reason, "protocol") {
		t.Fatalf("reason = %q; want a user-facing message without the protocol error text", reason)
	}
	if !strings.Contains(reason, "旧版") || !strings.Contains(reason, "升级") {
		t.Fatalf("reason = %q; want it to name the older helper and the upgrade", reason)
	}
}
