package supervisor

import (
	"context"
	"errors"
	"testing"
)

func settleWith(running RunningHelper, inspectErr, stopErr error) (StateDirChoice, *int) {
	stops := 0
	choice := SettleStateDir(context.Background(), "/shared", "/legacy", StateDirSteps{
		Inspect: func(_ context.Context, dir string) (RunningHelper, error) {
			if dir != "/legacy" {
				panic("inspected " + dir)
			}
			return running, inspectErr
		},
		Stop: func(context.Context, string, RunningHelper) error { stops++; return stopErr },
	})
	return choice, &stops
}

func TestSettleStateDirUsesSharedWithoutLegacy(t *testing.T) {
	for _, legacy := range []string{"", "/shared"} {
		choice := SettleStateDir(context.Background(), "/shared", legacy, StateDirSteps{})
		if choice.Dir != "/shared" || choice.Legacy {
			t.Fatalf("legacy %q: %+v", legacy, choice)
		}
	}
}

func TestSettleStateDirSharedWhenNoLegacyHelperRuns(t *testing.T) {
	choice, stops := settleWith(RunningHelper{}, ErrHelperNotRunning, nil)
	if choice.Dir != "/shared" || choice.Legacy || *stops != 0 {
		t.Fatalf("%+v stops=%d", choice, *stops)
	}
}

func TestSettleStateDirWaitsWhileLegacyHasSessions(t *testing.T) {
	running := RunningHelper{ActivityKnown: true, Activity: ActivitySnapshot{PTY: 2}}
	choice, stops := settleWith(running, nil, nil)
	if choice.Dir != "/legacy" || !choice.Legacy || choice.LegacySessions != 2 || !choice.LegacyKnown || *stops != 0 {
		t.Fatalf("%+v stops=%d", choice, *stops)
	}
}

func TestSettleStateDirWaitsWhenSessionsAreUnknown(t *testing.T) {
	choice, stops := settleWith(RunningHelper{ActivityKnown: false}, nil, nil)
	if choice.Dir != "/legacy" || choice.LegacyKnown || *stops != 0 {
		t.Fatalf("%+v stops=%d", choice, *stops)
	}
}

func TestSettleStateDirStopsIdleLegacyAndSwitches(t *testing.T) {
	choice, stops := settleWith(RunningHelper{ActivityKnown: true}, nil, nil)
	if choice.Dir != "/shared" || choice.Legacy || !choice.Stopped || *stops != 1 {
		t.Fatalf("%+v stops=%d", choice, *stops)
	}
}

func TestSettleStateDirStaysWhenIdleLegacyWillNotStop(t *testing.T) {
	boom := errors.New("boom")
	choice, _ := settleWith(RunningHelper{ActivityKnown: true}, nil, boom)
	if choice.Dir != "/legacy" || !errors.Is(choice.Err, boom) {
		t.Fatalf("%+v", choice)
	}
}

func TestSettleStateDirStaysWhenLegacyDoesNotAnswer(t *testing.T) {
	hung := errors.New("hello timed out")
	choice, stops := settleWith(RunningHelper{}, hung, nil)
	if choice.Dir != "/legacy" || !errors.Is(choice.Err, hung) || *stops != 0 {
		t.Fatalf("%+v stops=%d", choice, *stops)
	}
}

func TestSettleStateDirRealDefaultsFindNoHelper(t *testing.T) {
	choice := SettleStateDir(context.Background(), t.TempDir()+"/shared", t.TempDir()+"/legacy", StateDirSteps{})
	if choice.Legacy || choice.Err != nil {
		t.Fatalf("%+v", choice)
	}
}

func TestNewerThan(t *testing.T) {
	helper := func(version string, protocol int) RunningHelper {
		return RunningHelper{Protocol: protocol, Runtime: &RuntimeMetadata{Version: version}}
	}
	cases := []struct {
		name    string
		helper  RunningHelper
		local   string
		wantNew bool
	}{
		{"newer release", helper("1.4.1", ProtocolVersion), "1.4.0", true},
		{"v prefix", helper("v1.5.0", ProtocolVersion), "1.4.0", true},
		{"older release", helper("1.3.0", ProtocolVersion), "1.4.0", false},
		{"same version", helper("1.4.0", ProtocolVersion), "1.4.0", false},
		{"rc is older than stable", helper("1.4.0-rc.1", ProtocolVersion), "1.4.0", false},
		{"dev helper", helper("dev", ProtocolVersion), "1.4.0", false},
		{"dev local", helper("1.4.0", ProtocolVersion), "dev", false},
		{"other protocol", helper("9.0.0", ProtocolVersion-1), "1.4.0", false},
		{"no runtime", RunningHelper{Protocol: ProtocolVersion}, "1.4.0", false},
	}
	for _, tc := range cases {
		if got := tc.helper.NewerThan(RuntimeMetadata{Version: tc.local}); got != tc.wantNew {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestNewerHelperIsNotStaleAndNotReplaced(t *testing.T) {
	running := RunningHelper{
		Protocol: ProtocolVersion, ActivityKnown: true,
		Runtime: &RuntimeMetadata{Version: "2.0.0", SHA256: "aa"},
	}
	local := RuntimeMetadata{Version: "1.0.0", SHA256: "bb"}
	build := describeHelperBuild(running, local, StartEnv{})
	if build.Stale || !build.Newer || build.ConsentToken != "" || build.Notice() != "" {
		t.Fatalf("newer helper: %+v", build)
	}
	running.Runtime.Version = "0.9.0"
	build = describeHelperBuild(running, local, StartEnv{})
	if !build.Stale || build.Newer || build.StaleReason != "build" {
		t.Fatalf("older helper: %+v", build)
	}
}

func TestSameReleaseOfAnotherExecutableIsNotStale(t *testing.T) {
	helper := RunningHelper{Protocol: ProtocolVersion, ActivityKnown: true,
		Runtime: &RuntimeMetadata{Version: "1.4.0", Commit: "abc", SHA256: "aa"}}
	local := RuntimeMetadata{Version: "1.4.0", Commit: "abc", SHA256: "bb"}
	build := describeHelperBuild(helper, local, StartEnv{})
	if build.Stale || build.Newer || build.ConsentToken != "" {
		t.Fatalf("same release, other executable: %+v", build)
	}
	for name, other := range map[string]RuntimeMetadata{
		"other commit":  {Version: "1.4.0", Commit: "def", SHA256: "bb"},
		"other version": {Version: "1.5.0", Commit: "abc", SHA256: "bb"},
		"dev build":     {Version: "dev", Commit: "unknown", SHA256: "bb"},
		"dirty":         {Version: "1.4.0", Commit: "abc+dirty", SHA256: "bb"},
		"placeholder":   {Version: "0.0.0", Commit: "abc", SHA256: "bb"},
	} {
		if build := describeHelperBuild(helper, other, StartEnv{}); build.Newer == build.Stale && !build.Stale {
			t.Errorf("%s: %+v", name, build)
		}
	}
	devHelper := RunningHelper{Protocol: ProtocolVersion, Runtime: &RuntimeMetadata{Version: "dev", Commit: "unknown", SHA256: "aa"}}
	if !describeHelperBuild(devHelper, RuntimeMetadata{Version: "dev", Commit: "unknown", SHA256: "bb"}, StartEnv{}).Stale {
		t.Fatal("dev builds with different digests stay stale")
	}
}
