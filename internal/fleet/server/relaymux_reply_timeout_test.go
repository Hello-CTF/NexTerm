package fleetserver

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/relaymux"
)

// A client waits for a device stream's reply longer than the server may wait for an older agent to dial its
// legacy bridge, so routing through the relay never fails where the per-operation bridge would have succeeded.
func TestRelayStreamReplyOutlastsTheLegacyBridgeWait(t *testing.T) {
	if relaymux.StreamReplyTimeout <= bridgeWaitTimeout {
		t.Fatalf("stream reply timeout %v must exceed the legacy bridge wait %v", relaymux.StreamReplyTimeout, bridgeWaitTimeout)
	}
}
