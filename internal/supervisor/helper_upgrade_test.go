package supervisor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestReplaceTransitionTableEndsEveryPathWithoutLoops(t *testing.T) {
	terminal := map[ReplaceStage]bool{ReplaceReady: true, ReplaceFailed: true}
	for stage, next := range replaceTransitions {
		if terminal[stage] != (len(next) == 0) {
			t.Fatalf("stage %s: terminal %v but %d transitions", stage, terminal[stage], len(next))
		}
		for _, target := range next {
			if _, ok := replaceTransitions[target]; !ok {
				t.Fatalf("transition %s -> %s leads to an undefined stage", stage, target)
			}
		}
	}
	// Every path from verify is finite and ends in a terminal stage.
	var walk func(stage ReplaceStage, seen []ReplaceStage)
	walk = func(stage ReplaceStage, seen []ReplaceStage) {
		if slices.Contains(seen, stage) {
			t.Fatalf("replacement can loop: %v -> %s", seen, stage)
		}
		next := replaceTransitions[stage]
		if len(next) == 0 && !terminal[stage] {
			t.Fatalf("path %v ends in non-terminal %s", seen, stage)
		}
		for _, target := range next {
			walk(target, append(slices.Clone(seen), stage))
		}
	}
	walk(ReplaceVerify, nil)
}

func TestHelperConsentTokenRoundTripsAndCovers(t *testing.T) {
	for _, helper := range []RunningHelper{
		{Protocol: 2, PID: 41, Activity: ActivitySnapshot{PTY: 2, Exec: 1}, ActivityKnown: true},
		{Protocol: 2, PID: 41, Activity: ActivitySnapshot{PTY: 2}, PTYListed: true},
		{Protocol: 2, PID: 41},
	} {
		parsed, ok := ParseHelperConsent(HelperConsentToken(helper))
		if !ok || !ConsentCovers(parsed, helper) {
			t.Fatalf("token of %+v = %+v, %v; want it to cover the helper", helper, parsed, ok)
		}
	}
	consent := RunningHelper{Protocol: 2, PID: 41, Activity: ActivitySnapshot{PTY: 2}, ActivityKnown: true}
	for name, current := range map[string]RunningHelper{
		"more sessions":    {Protocol: 2, PID: 41, Activity: ActivitySnapshot{PTY: 2, Exec: 1}, ActivityKnown: true},
		"another pid":      {Protocol: 2, PID: 42, Activity: ActivitySnapshot{PTY: 1}, ActivityKnown: true},
		"count now hidden": {Protocol: 2, PID: 41},
		"another protocol": {Protocol: 3, PID: 41, ActivityKnown: true},
	} {
		if ConsentCovers(consent, current) {
			t.Fatalf("%s: consent %+v covered %+v", name, consent, current)
		}
	}
	if !ConsentCovers(consent, RunningHelper{Protocol: 2, PID: 41, Activity: ActivitySnapshot{PTY: 1}, ActivityKnown: true}) {
		t.Fatal("consent did not cover a helper whose sessions dropped since")
	}
	for _, token := range []string{"", "2:41:1", "2:41:1:3", "0:41:1:1", "2:0:1:1", "2:41:-1:1", "x:41:1:1"} {
		if _, ok := ParseHelperConsent(token); ok {
			t.Fatalf("malformed token %q parsed", token)
		}
	}
}

type replaceOutcome struct {
	verify     error // nil, ErrHelperNotRunning, a stale helper or another error
	stale      bool
	executable bool
	stop       error
	start      error
	restore    error
}

// TestReplaceStepsFollowTheTable runs every combination of step outcomes and
// checks the stages taken, what was called and the reported result.
var (
	inspectErr = errors.New("inspect failed")
	stopErr    = errors.New("stop failed")
	startErr   = errors.New("start failed")
	restoreErr = errors.New("restore failed")
)

func TestReplaceStepsFollowTheTable(t *testing.T) {
	consent := RunningHelper{Protocol: 2, PID: 7, Activity: ActivitySnapshot{PTY: 1}, ActivityKnown: true}
	for _, verify := range []error{nil, ErrHelperNotRunning, inspectErr} {
		for _, stale := range []bool{false, true} {
			for _, executable := range []bool{false, true} {
				for _, stop := range []error{nil, stopErr} {
					for _, start := range []error{nil, startErr, ErrLegacyHelperRunning} {
						for _, restore := range []error{nil, restoreErr} {
							outcome := replaceOutcome{verify: verify, stale: stale, executable: executable, stop: stop, start: start, restore: restore}
							t.Run(fmt.Sprintf("%+v", outcome), func(t *testing.T) {
								checkReplaceOutcome(t, consent, outcome)
							})
						}
					}
				}
			}
		}
	}
}

func checkReplaceOutcome(t *testing.T, consent RunningHelper, outcome replaceOutcome) {
	var calls []string
	steps := replaceSteps{
		verify: func(context.Context, int) (RunningHelper, error) {
			calls = append(calls, "verify")
			if outcome.verify != nil {
				return RunningHelper{}, outcome.verify
			}
			current := consent
			if outcome.stale {
				current.Activity.Exec = 1
			}
			return current, nil
		},
		executable: func(int) (string, error) {
			if outcome.executable {
				return "/old/helper", nil
			}
			return "", ErrUnsupported
		},
		stop: func(context.Context, RunningHelper) error {
			calls = append(calls, "stop")
			return outcome.stop
		},
		start: func(context.Context) (*Helper, error) {
			calls = append(calls, "start")
			if outcome.start != nil {
				return nil, outcome.start
			}
			return &Helper{}, nil
		},
		restore: func(_ context.Context, executable string, protocol int) error {
			calls = append(calls, "restore")
			if executable != "/old/helper" || protocol != consent.Protocol {
				t.Fatalf("restore(%q, %d), want the old helper's executable and protocol", executable, protocol)
			}
			return outcome.restore
		},
	}
	helper, trace, err := steps.run(context.Background(), consent)
	for index := 1; index < len(trace); index++ {
		if !slices.Contains(replaceTransitions[trace[index-1]], trace[index]) {
			t.Fatalf("trace %v takes %s -> %s outside the table", trace, trace[index-1], trace[index])
		}
	}
	last := trace[len(trace)-1]
	if (last == ReplaceReady) != (err == nil) || (helper != nil) != (err == nil) || last != ReplaceReady && last != ReplaceFailed {
		t.Fatalf("trace %v ended with helper %v, error %v", trace, helper, err)
	}
	var stale *LegacyHelperError
	var failed *HelperReplaceError
	switch {
	case errors.Is(outcome.verify, inspectErr):
		if !errors.As(err, &failed) || failed.Stage != ReplaceVerify || slices.Contains(calls, "stop") {
			t.Fatalf("inspection failure = %v after %v, want a verify failure that stops nothing", err, calls)
		}
	case outcome.verify == nil && outcome.stale:
		if !errors.As(err, &stale) || !stale.Stale || stale.ConsentToken == "" || slices.Contains(calls, "stop") {
			t.Fatalf("stale consent = %v after %v, want a fresh LegacyHelperError and nothing stopped", err, calls)
		}
	case outcome.verify == nil && outcome.stop != nil:
		if !errors.As(err, &failed) || failed.Stage != ReplaceTerminate || slices.Contains(calls, "start") {
			t.Fatalf("stop failure = %v after %v, want a terminate failure that starts nothing", err, calls)
		}
	case outcome.start == nil:
		if err != nil {
			t.Fatalf("replacement = %v after %v, want success", err, calls)
		}
		if outcome.verify != nil && slices.Contains(calls, "stop") {
			t.Fatal("a helper that already exited was stopped")
		}
	default:
		restoring := outcome.verify == nil && outcome.executable && !errors.Is(outcome.start, ErrLegacyHelperRunning)
		if slices.Contains(calls, "restore") != restoring {
			t.Fatalf("calls %v, restore expected %v", calls, restoring)
		}
		if !errors.As(err, &failed) || failed.Stage != ReplaceStart || !errors.Is(err, ErrHelperReplaceFailed) ||
			failed.RolledBack != (restoring && outcome.restore == nil) {
			t.Fatalf("start failure = %#v, want a start failure, rolled back %v", err, restoring && outcome.restore == nil)
		}
	}
	if count := len(calls); count > 4 {
		t.Fatalf("calls %v: a replacement runs each step at most once", calls)
	}
}
