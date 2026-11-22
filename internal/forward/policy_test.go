package forward

import (
	"errors"
	"testing"
)

func TestPolicyEnvironment(t *testing.T) {
	tests := []struct {
		name   string
		policy Policy
		host   string
	}{
		{name: "desktop", policy: Policy{Desktop: true}, host: "127.0.0.1"},
		{name: "server", policy: Policy{}, host: "0.0.0.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := test.policy.Environment()
			if environment.ListenHost != test.host {
				t.Fatalf("environment = %+v", environment)
			}
		})
	}
}

func TestServiceRejectsAfterClose(t *testing.T) {
	provider := &switchProvider{dialer: &recordingDialer{}}
	service := newLoopbackService(t, provider)
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "s", TargetHost: "example.invalid", TargetPort: 80})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("CreateLocal error = %v", err)
	}
	if err := service.Start(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start error = %v", err)
	}
}
