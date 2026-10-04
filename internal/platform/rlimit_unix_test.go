//go:build unix

package platform

import (
	"syscall"
	"testing"
)

func TestRaiseNoFileLimitReachesMinOfHardAndTarget(t *testing.T) {
	var before syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &before); err != nil {
		t.Fatal(err)
	}
	raised, err := RaiseNoFileLimit(DefaultNoFileLimit)
	if err != nil {
		t.Fatal(err)
	}
	var after syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &after); err != nil {
		t.Fatal(err)
	}
	if after.Cur != raised {
		t.Fatalf("soft limit = %d, RaiseNoFileLimit reported %d", after.Cur, raised)
	}
	if raised < before.Cur {
		t.Fatalf("soft limit was lowered from %d to %d", before.Cur, raised)
	}
	if after.Cur > after.Max {
		t.Fatalf("soft limit %d exceeds hard limit %d", after.Cur, after.Max)
	}
	desired := uint64(DefaultNoFileLimit)
	if after.Max < desired {
		desired = after.Max
	}
	if after.Cur != desired && before.Cur < desired {
		t.Fatalf("soft limit = %d, want %d (hard %d)", after.Cur, desired, after.Max)
	}
}

func TestRaiseNoFileLimitNeverLowers(t *testing.T) {
	var before syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &before); err != nil {
		t.Fatal(err)
	}
	raised, err := RaiseNoFileLimit(1)
	if err != nil {
		t.Fatal(err)
	}
	if raised != before.Cur {
		t.Fatalf("RaiseNoFileLimit(1) = %d, want current %d", raised, before.Cur)
	}
}
