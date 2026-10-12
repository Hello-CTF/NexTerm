//go:build !ios

package biometric

import (
	"errors"
	"testing"
)

func TestStubReportsUnavailable(t *testing.T) {
	status, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if status.Available || status.CanEvaluate || status.HasMaterial {
		t.Fatalf("stub status = %+v, want all false", status)
	}
	if _, err := Unlock(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stub Unlock err = %v, want ErrUnavailable", err)
	}
	if err := Enable("pw"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stub Enable err = %v, want ErrUnavailable", err)
	}
	if err := Disable(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stub Disable err = %v, want ErrUnavailable", err)
	}
}
