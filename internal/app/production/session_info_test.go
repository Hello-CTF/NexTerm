package production

import (
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

func TestProductionSessionInfoIncludesReconnectMetadata(t *testing.T) {
	next := time.UnixMilli(1_700_000_000_123)
	info := productionSessionInfo(session.SessionInfo{
		ID: "s1", AssetID: "asset-1", Name: "host", Kind: "ssh", Status: session.StatusReconnecting,
		Tabs: []string{"tab-1"}, CreatedAt: time.UnixMilli(42), Error: "network unreachable", ErrorCode: ipc.CodeDisconnected,
		Attempt: 3, NextRetryAt: &next,
	})
	if info.Error != "network unreachable" || info.ErrorCode != ipc.CodeDisconnected || info.Attempt != 3 {
		t.Fatalf("reconnect metadata = %+v", info)
	}
	if info.NextRetryAt == nil || !info.NextRetryAt.Equal(next) {
		t.Fatalf("next retry = %v, want %v", info.NextRetryAt, next)
	}
}
