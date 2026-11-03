package alerts

import (
	"testing"
	"time"
)

const minMS = int64(60_000)

// series builds samples every step ms ending at end, covering span ms back, all with value v.
func series(end, span, step int64, v float64) []Sample {
	var out []Sample
	for ts := end - span; ts <= end; ts += step {
		out = append(out, Sample{TS: ts, Value: v})
	}
	return out
}

func TestClassify(t *testing.T) {
	const now = int64(10_000_000_000)
	sustain := 10 * time.Minute
	gap := GapTolerance(DefaultInterval)
	with := func(base []Sample, mut func([]Sample) []Sample) []Sample { return mut(base) }
	cases := []struct {
		name    string
		samples []Sample
		want    Verdict
	}{
		{"sustained breach", series(now, 10*minMS, minMS, 95), VerdictBreach},
		{"breach longer than needed", series(now, 14*minMS, minMS, 95), VerdictBreach},
		{"duration too short", series(now, 9*minMS, minMS, 95), VerdictUnknown},
		{"equal to threshold is not a breach", series(now, 10*minMS, minMS, 90), VerdictClear},
		{"latest sample stale", series(now-3*minMS, 14*minMS, minMS, 95), VerdictUnknown},
		{"latest within G still fresh", series(now-2*minMS, 14*minMS, minMS, 95), VerdictBreach},
		{"gap larger than G", with(series(now, 12*minMS, minMS, 95), func(s []Sample) []Sample {
			var out []Sample
			for _, x := range s {
				if x.TS > now-8*minMS && x.TS < now-4*minMS {
					continue
				}
				out = append(out, x)
			}
			return out
		}), VerdictUnknown},
		{"gap within G allowed", with(series(now, 12*minMS, minMS, 95), func(s []Sample) []Sample {
			var out []Sample
			for _, x := range s {
				if x.TS == now-5*minMS {
					continue
				}
				out = append(out, x)
			}
			return out
		}), VerdictBreach},
		{"dip inside window", with(series(now, 12*minMS, minMS, 95), func(s []Sample) []Sample {
			s[6].Value = 80
			return s
		}), VerdictUnknown},
		{"dip before anchor is irrelevant", with(series(now, 14*minMS, minMS, 95), func(s []Sample) []Sample {
			s[0].Value = 10
			return s
		}), VerdictBreach},
		{"sustained clear", series(now, 5*minMS, minMS, 50), VerdictClear},
		{"clear equal to threshold", series(now, 5*minMS, minMS, 90), VerdictClear},
		{"clear too short", series(now, 4*minMS, minMS, 50), VerdictUnknown},
		{"stale clear does not resolve", series(now-3*minMS, 8*minMS, minMS, 50), VerdictUnknown},
		{"no samples", nil, VerdictUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := Classify(tc.samples, now, 90, sustain, gap)
			if got != tc.want {
				t.Fatalf("verdict = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyReturnsLatestValue(t *testing.T) {
	const now = int64(10_000_000_000)
	s := series(now, 10*minMS, minMS, 95)
	s[len(s)-1].Value = 97
	_, v := Classify(s, now, 90, 10*time.Minute, 2*time.Minute)
	if v != 97 {
		t.Fatalf("value = %v", v)
	}
}

func TestStepThreshold(t *testing.T) {
	const t0 = int64(1_000_000_000_000)
	in := func(now int64, v Verdict) StepInput {
		return StepInput{Now: now, Enabled: true, Verdict: v, Value: 95, Threshold: 90}
	}

	t.Run("ok to firing emits once", func(t *testing.T) {
		st, ev := StepThreshold(State{Status: StatusOK}, in(t0, VerdictBreach))
		if st.Status != StatusFiring || st.FiredAt != t0 || st.Since != t0 || ev == nil || ev.Kind != KindFired {
			t.Fatalf("st=%+v ev=%+v", st, ev)
		}
		st2, ev2 := StepThreshold(st, in(t0+minMS, VerdictBreach))
		if ev2 != nil || st2.Status != StatusFiring || st2.FiredAt != t0 {
			t.Fatalf("repeat fired: st=%+v ev=%+v", st2, ev2)
		}
		st3, ev3 := StepThreshold(st, in(t0+minMS, VerdictUnknown))
		if ev3 != nil || st3.Status != StatusFiring {
			t.Fatalf("unknown changed state: %+v %+v", st3, ev3)
		}
	})

	t.Run("firing to ok resolves with duration", func(t *testing.T) {
		firing := State{Status: StatusFiring, Since: t0, FiredAt: t0}
		now := t0 + 30*minMS
		st, ev := StepThreshold(firing, StepInput{Now: now, Enabled: true, Verdict: VerdictClear, Value: 40, Threshold: 90})
		if st.Status != StatusOK || st.ResolvedAt != now || ev == nil || ev.Kind != KindResolved || ev.DurationMS != 30*minMS {
			t.Fatalf("st=%+v ev=%+v", st, ev)
		}
	})

	t.Run("cooldown suppresses refire then allows", func(t *testing.T) {
		resolved := State{Status: StatusOK, ResolvedAt: t0}
		st, ev := StepThreshold(resolved, in(t0+29*minMS, VerdictBreach))
		if ev != nil || st.Status != StatusOK {
			t.Fatalf("fired inside cooldown: %+v %+v", st, ev)
		}
		st, ev = StepThreshold(resolved, in(t0+30*minMS, VerdictBreach))
		if ev == nil || ev.Kind != KindFired || st.Status != StatusFiring {
			t.Fatalf("no fire after cooldown: %+v %+v", st, ev)
		}
		st, ev = StepThreshold(resolved, in(t0+31*minMS, VerdictUnknown))
		if ev != nil || st.Status != StatusOK {
			t.Fatalf("unknown after cooldown fired: %+v %+v", st, ev)
		}
	})

	t.Run("disabled resets silently and keeps cooldown base", func(t *testing.T) {
		firing := State{Status: StatusFiring, Since: t0, FiredAt: t0, ResolvedAt: t0 - 99}
		st, ev := StepThreshold(firing, StepInput{Now: t0 + minMS, Enabled: false, Verdict: VerdictBreach, Value: 99, Threshold: 90})
		if ev != nil || st.Status != StatusOK || st.ResolvedAt != t0-99 {
			t.Fatalf("st=%+v ev=%+v", st, ev)
		}
		st, ev = StepThreshold(State{Status: StatusOK}, StepInput{Now: t0, Enabled: false, Verdict: VerdictBreach, Value: 99, Threshold: 90})
		if ev != nil || st.Status != StatusOK {
			t.Fatalf("disabled ok fired: %+v %+v", st, ev)
		}
	})
}

func TestResolveSettings(t *testing.T) {
	f := false
	th := 70.0
	cases := []struct {
		name    string
		rule    Rule
		set     *Settings
		enabled bool
		thr     float64
	}{
		{"defaults", RuleDisk, nil, true, 90},
		{"cpu default", RuleCPU, &Settings{}, true, 90},
		{"override threshold", RuleMem, &Settings{MemThreshold: &th}, true, 70},
		{"disable", RuleCPU, &Settings{CPUEnabled: &f}, false, 90},
		{"other rule untouched", RuleDisk, &Settings{CPUEnabled: &f, MemThreshold: &th}, true, 90},
		{"invalid threshold falls back", RuleDisk, &Settings{DiskThreshold: ptr(0.0)}, true, 90},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			en, thr := ResolveThreshold(tc.rule, tc.set)
			if en != tc.enabled || thr != tc.thr {
				t.Fatalf("got %v %v", en, thr)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestSampleValue(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	f := 55.0
	m := Metric{CPUPct: &f, MemUsed: i(50), MemTotal: i(200), DiskUsed: i(9), DiskTotal: i(10)}
	if v, ok := metricValue(&m, RuleCPU); !ok || v != 55 {
		t.Fatalf("cpu %v %v", v, ok)
	}
	if v, ok := metricValue(&m, RuleMem); !ok || v != 25 {
		t.Fatalf("mem %v %v", v, ok)
	}
	if v, ok := metricValue(&m, RuleDisk); !ok || v != 90 {
		t.Fatalf("disk %v %v", v, ok)
	}
	bad := []Metric{
		{},
		{MemUsed: i(1), MemTotal: i(0), DiskUsed: i(1), DiskTotal: i(-5)},
		{MemUsed: i(1), DiskUsed: i(1)},
	}
	for _, b := range bad {
		for _, r := range []Rule{RuleCPU, RuleMem, RuleDisk} {
			if _, ok := metricValue(&b, r); ok {
				t.Fatalf("%v accepted %+v", r, b)
			}
		}
	}
}

func TestGapTolerance(t *testing.T) {
	if got := GapTolerance(DefaultInterval); got != 150*time.Second {
		t.Fatalf("GapTolerance = %v, want 2.5 intervals", got)
	}
	if got := GapTolerance(time.Second); got != 2500*time.Millisecond {
		t.Fatalf("GapTolerance(1s) = %v, want no integer truncation", got)
	}
}

func TestClassifyGapBoundary(t *testing.T) {
	const (
		now = int64(10_000_000_000)
		sec = int64(1000)
	)
	gap := GapTolerance(DefaultInterval)
	// A 12-minute run of 1-minute samples, with the samples in (now-ago, ...) rewritten as asked.
	run := func(latestAgo int64) []Sample { return series(now-latestAgo, 12*minMS, minMS, 95) }
	holes := func(gapMS int64) []Sample {
		// One hole of gapMS between two samples, early enough to sit inside the sustain window.
		a, b := now-8*minMS, now-8*minMS+gapMS
		var out []Sample
		for ts := now - 12*minMS; ts < a; ts += minMS {
			out = append(out, Sample{TS: ts, Value: 95})
		}
		out = append(out, Sample{TS: a, Value: 95})
		for ts := b; ts <= now; ts += minMS {
			out = append(out, Sample{TS: ts, Value: 95})
		}
		return out
	}
	cases := []struct {
		name    string
		samples []Sample
		want    Verdict
	}{
		{"latest exactly 150s old is fresh", run(150 * sec), VerdictBreach},
		{"latest 151s old is unknown", run(151 * sec), VerdictUnknown},
		{"interval exactly 150s holds", holes(150 * sec), VerdictBreach},
		{"interval 151s breaks", holes(151 * sec), VerdictUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := Classify(tc.samples, now, 90, 10*time.Minute, gap); got != tc.want {
				t.Fatalf("verdict = %v, want %v", got, tc.want)
			}
		})
	}
}
