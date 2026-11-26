package leg

import (
	"testing"
	"time"
)

func TestLossSamplerWindow(t *testing.T) {
	var s lossSampler
	t0 := time.Unix(1000, 0)
	// 100 packets/s; 10 lost in the first second only.
	s.add(lossSample{at: t0, sent: 0, lost: 0})
	s.add(lossSample{at: t0.Add(time.Second), sent: 100, lost: 10})
	for i := 2; i <= 20; i++ {
		s.add(lossSample{at: t0.Add(time.Duration(i) * time.Second), sent: uint64(100 * i), lost: 10})
	}

	early := lossSample{at: t0.Add(5 * time.Second), sent: 500, lost: 10}
	var fresh lossSampler
	fresh.add(lossSample{at: t0})
	if got := fresh.loss(early); got != 10.0/500 {
		t.Fatalf("young leg loss = %v, want %v", got, 10.0/500)
	}

	now := lossSample{at: t0.Add(20 * time.Second), sent: 2000, lost: 10}
	if got := s.loss(now); got != 0 {
		t.Fatalf("loss outside the window still counted: %v", got)
	}
	now = lossSample{at: t0.Add(20 * time.Second), sent: 2000, lost: 30}
	if got := s.loss(now); got != 20.0/1000 {
		t.Fatalf("window loss = %v, want %v", got, 20.0/1000)
	}
	if n := len(s.samples); n > int(LossWindow/time.Second)+2 {
		t.Fatalf("sampler keeps %d samples", n)
	}
	// Lost counters can shrink (late arrivals); never report negative loss.
	if got := s.loss(lossSample{at: t0.Add(20 * time.Second), sent: 2000, lost: 0}); got != 0 {
		t.Fatalf("negative delta gave %v", got)
	}
}
