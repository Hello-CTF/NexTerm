package execbatch

import (
	"strings"
	"testing"
)

func TestRerunEligibleStatuses(t *testing.T) {
	for _, test := range []struct {
		status Status
		want   bool
	}{
		{StatusSuccess, false},
		{StatusExitError, true},
		{StatusTransportError, true},
		{StatusTimeout, true},
		{StatusCanceled, false},
		{Status("weird"), false},
		{"", false},
	} {
		if got := RerunEligible(test.status); got != test.want {
			t.Fatalf("RerunEligible(%q) = %v, want %v", test.status, got, test.want)
		}
	}
}

func TestSelectRerunTargetsKeepsFailedOnlyInOrder(t *testing.T) {
	exitCode := 2
	results := []Result{
		{SessionID: "ok-1", Status: StatusSuccess},
		{SessionID: "exit", Status: StatusExitError, ExitCode: &exitCode},
		{SessionID: "ok-2", Status: StatusSuccess},
		{SessionID: "transport", Status: StatusTransportError},
		{SessionID: "canceled", Status: StatusCanceled},
		{SessionID: "timeout", Status: StatusTimeout},
	}
	selected := SelectRerunTargets(results)
	if strings.Join(selected, ",") != "exit,transport,timeout" {
		t.Fatalf("selected = %+v", selected)
	}
}

func TestSelectRerunTargetsEmptyWhenNothingFailed(t *testing.T) {
	if selected := SelectRerunTargets(nil); selected == nil || len(selected) != 0 {
		t.Fatalf("nil results selected = %#v, want empty", selected)
	}
	successes := []Result{
		{SessionID: "ok-1", Status: StatusSuccess},
		{SessionID: "ok-2", Status: StatusSuccess},
	}
	if selected := SelectRerunTargets(successes); len(selected) != 0 {
		t.Fatalf("all-success selected = %+v", selected)
	}
	canceled := []Result{
		{SessionID: "stop-1", Status: StatusCanceled},
		{SessionID: "stop-2", Status: StatusCanceled},
	}
	if selected := SelectRerunTargets(canceled); len(selected) != 0 {
		t.Fatalf("canceled-only selected = %+v", selected)
	}
}
