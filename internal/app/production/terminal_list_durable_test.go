//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fixedDescriptorListProvider struct {
	base.DurableProvider
	descriptors []base.DurableDescriptor
}

func (p fixedDescriptorListProvider) ListDurableDescriptors(context.Context) ([]base.DurableDescriptor, error) {
	return p.descriptors, nil
}

func TestProductionListDurableCarriesStateAndActivity(t *testing.T) {
	created := time.UnixMilli(1_700_000_000_000)
	output := created.Add(3 * time.Minute)
	detached := created.Add(5 * time.Minute)
	asOf := created.Add(9 * time.Minute)
	outputAge, detachedAge := asOf.Sub(output).Milliseconds(), asOf.Sub(detached).Milliseconds()
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixedDescriptorListProvider{DurableProvider: fixture.provider, descriptors: []base.DurableDescriptor{
		{
			ID: "current", Incarnation: "inc-current", CreatedAt: created, Cols: 120, Rows: 40,
			State:    &base.SessionState{State: base.SessionNeedsMe, Confidence: base.ConfidenceSignal},
			Activity: &base.DurableActivity{LastOutputAt: &output, DetachedAt: &detached, AsOf: asOf, LastOutputMSAgo: &outputAge, DetachedMSAgo: &detachedAge},
		},
		{
			ID: "quiet", Incarnation: "inc-quiet", CreatedAt: created, Cols: 80, Rows: 24,
			State:    &base.SessionState{State: base.SessionIdle},
			Activity: &base.DurableActivity{Attached: true},
		},
		// A helper that predates state and activity reporting.
		{ID: "old", Incarnation: "inc-old", CreatedAt: created, Cols: 80, Rows: 24},
	}}}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)

	response := dispatchDurableTest(t, production, "terminal_list", `{"sessionId":"`+connected.ID+`","durable":true}`, "", "client-a")
	var descriptors []map[string]json.RawMessage
	requireStoreTestResponse(t, response, &descriptors)
	if len(descriptors) != 3 {
		t.Fatalf("descriptors = %s", response.Data)
	}
	field := func(index int, name string) string {
		return string(descriptors[index][name])
	}
	if got := field(0, "state"); got != `"needs_me"` {
		t.Fatalf("current state = %s", got)
	}
	var activity struct {
		LastOutputAt *int64 `json:"lastOutputAt"`
		DetachedAt   *int64 `json:"detachedAt"`
		Attached     bool   `json:"attached"`
		AsOf         *int64 `json:"asOf"`
		OutputAge    *int64 `json:"lastOutputMsAgo"`
		DetachedAge  *int64 `json:"detachedMsAgo"`
	}
	if err := json.Unmarshal(descriptors[0]["activity"], &activity); err != nil {
		t.Fatal(err)
	}
	if activity.LastOutputAt == nil || *activity.LastOutputAt != output.UnixMilli() ||
		activity.DetachedAt == nil || *activity.DetachedAt != detached.UnixMilli() || activity.Attached ||
		activity.AsOf == nil || *activity.AsOf != asOf.UnixMilli() ||
		activity.OutputAge == nil || *activity.OutputAge != 6*60_000 || activity.DetachedAge == nil || *activity.DetachedAge != 4*60_000 {
		t.Fatalf("current activity = %s", descriptors[0]["activity"])
	}
	if got := field(0, "createdAt"); got != "1700000000000" {
		t.Fatalf("created at = %s", got)
	}
	if got := field(1, "activity"); got != `{"lastOutputAt":null,"detachedAt":null,"attached":true}` {
		t.Fatalf("attached activity = %s", got)
	}
	if _, ok := descriptors[2]["state"]; ok {
		t.Fatalf("an old helper reports state %s", descriptors[2]["state"])
	}
	if _, ok := descriptors[2]["activity"]; ok {
		t.Fatalf("an old helper reports activity %s, want it absent", descriptors[2]["activity"])
	}
}
