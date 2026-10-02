package session

import (
	"encoding/json"
	"testing"
)

func TestControllerJSONUsesExplicitNullWithoutDroppingSnapshotKeys(t *testing.T) {
	tests := []struct {
		name     string
		empty    any
		occupied any
		required []string
	}{
		{
			name:     "tab info",
			empty:    TabInfo{ID: "tab", SessionID: "session", Controller: "", Subscribers: 0, Viewers: 0, Exited: true},
			occupied: TabInfo{ID: "tab", SessionID: "session", Controller: "client-a", Subscribers: 1, Viewers: 1},
			required: []string{"id", "sessionId", "cols", "rows", "gridRevision", "controller", "subscribers", "viewers", "exited"},
		},
		{
			name:     "control event",
			empty:    ControlEvent{TabID: "tab", Controller: "", Subscribers: 0, Viewers: 0, Exited: true, Version: 4},
			occupied: ControlEvent{TabID: "tab", Controller: "client-a", Subscribers: 2, Viewers: 1, Version: 5},
			required: []string{"tabId", "cols", "rows", "gridRevision", "controller", "subscribers", "viewers", "exited", "version"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			empty := marshalWireObject(t, test.empty)
			controller, present := empty["controller"]
			if !present || controller != nil {
				t.Fatalf("empty controller = %#v (present=%v), want explicit null", controller, present)
			}
			for _, key := range test.required {
				if _, present := empty[key]; !present {
					t.Errorf("full snapshot omitted %q: %#v", key, empty)
				}
			}
			occupied := marshalWireObject(t, test.occupied)
			if got := occupied["controller"]; got != "client-a" {
				t.Fatalf("occupied controller = %#v", got)
			}
		})
	}
}

func marshalWireObject(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	return object
}
