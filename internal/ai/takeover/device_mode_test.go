package takeover

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

// The takeover run must apply the guard mode bound to the real target asset
// (resolved from the tab), not just the global mode: a device bound to
// read_only denies even what the global silent mode would auto-allow, a
// device bound to unattended never lets a confirmation-worthy write through,
// and a mode bound to a different asset changes nothing.
func TestTakeoverAppliesDeviceModeBoundToRealAsset(t *testing.T) {
	const probe = "touch /tmp/nexterm-guard-probe"
	for _, test := range []struct {
		name        string
		global      guard.Mode
		boundAsset  string
		boundMode   guard.Mode
		wantConfirm bool
	}{
		{"global silent yields to asset read_only", guard.Silent, "asset-1", guard.ReadOnly, false},
		{"global read_write yields to asset unattended", guard.ReadWrite, "asset-1", guard.Unattended, false},
		{"mode bound to another asset leaves the tab on the global mode", guard.ReadWrite, "asset-2", guard.ReadOnly, true},
		{"unbound asset keeps the global mode", guard.ReadWrite, "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarnessWith(t, sequence(
				toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"`+probe+`","enter":true}`)),
				toolCallMessage(doneCall("finished")),
			), func(deps *Dependencies) {
				deps.Permission = func(context.Context) (guard.Config, error) {
					return guard.Config{Mode: test.global}, nil
				}
				deps.TabAsset = func(context.Context, string) (string, error) { return "asset-1", nil }
				deps.DeviceMode = func(_ context.Context, config guard.Config, assetID string) (guard.Config, error) {
					if test.boundAsset != "" && assetID == test.boundAsset {
						config.Mode = test.boundMode
					}
					return config, nil
				}
			})
			stream := &agent.SliceStream{}
			response := h.run(t, stream, RunArgs{})
			if test.wantConfirm {
				confirmation := waitEvent(t, stream, "confirmRequired")
				if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "deny"}); err != nil {
					t.Fatal(err)
				}
			} else {
				event := waitEvent(t, stream, "toolResult")
				if event.OK || !strings.Contains(event.Text, "权限策略已拒绝") {
					t.Fatalf("denied write surfaced as %+v", event)
				}
			}
			waitClosed(t, stream)
			h.mu.Lock()
			writes := len(h.aiWrites)
			h.mu.Unlock()
			if writes != 0 {
				t.Fatalf("denied device-mode write reached the terminal %d time(s)", writes)
			}
		})
	}
}

// A device-mode resolution failure fails the run closed instead of silently
// falling back to the global mode.
func TestTakeoverFailsClosedWhenDeviceModeUnresolvable(t *testing.T) {
	h := newHarnessWith(t, sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/nexterm-guard-probe","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	), func(deps *Dependencies) {
		deps.TabAsset = func(context.Context, string) (string, error) { return "asset-1", nil }
		deps.DeviceMode = func(context.Context, guard.Config, string) (guard.Config, error) {
			return guard.Config{}, context.DeadlineExceeded
		}
	})
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	events := waitClosed(t, stream)
	done, failed := counts(events)
	if done != 0 || failed != 1 {
		t.Fatalf("events = %+v", events)
	}
	h.mu.Lock()
	writes := len(h.aiWrites)
	h.mu.Unlock()
	if writes != 0 {
		t.Fatal("write landed despite the unresolvable device mode")
	}
}
