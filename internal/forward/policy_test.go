package forward

import (
	"errors"
	"testing"
)

func TestPolicyEnvironment(t *testing.T) {
	tests := []struct {
		name      string
		policy    Policy
		available bool
		platform  string
		host      string
	}{
		{name: "desktop ignores server platform", policy: Policy{Desktop: true, Platform: "lazycat"}, available: true, platform: "other", host: "127.0.0.1"},
		{name: "server", policy: Policy{}, available: true, platform: "other", host: "0.0.0.0"},
		{name: "empty platform", policy: Policy{Platform: " "}, available: true, platform: "other", host: "0.0.0.0"},
		{name: "LazyCat case insensitive", policy: Policy{Platform: " LazyCat "}, available: false, platform: "lazycat", host: "0.0.0.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := test.policy.Environment()
			if environment.Available != test.available || environment.Platform != test.platform || environment.ListenHost != test.host {
				t.Fatalf("environment = %+v", environment)
			}
			if !test.available && environment.Reason == "" {
				t.Fatal("unavailable environment has no reason")
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
