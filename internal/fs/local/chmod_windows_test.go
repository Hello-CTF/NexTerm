//go:build windows

package local

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestChmodUnsupported(t *testing.T) {
	if err := New().Chmod(context.Background(), "file", 0o755); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("Chmod error = %v", err)
	}
}
