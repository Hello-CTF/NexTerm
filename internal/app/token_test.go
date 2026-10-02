package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type fakeTokenStore struct {
	token   string
	rotated string
	reads   int
	rotates int
}

func (s *fakeTokenStore) SyncToken(context.Context) (string, error) {
	s.reads++
	return s.token, nil
}

func (s *fakeTokenStore) RotateSyncToken(context.Context) (string, error) {
	s.rotates++
	return s.rotated, nil
}

func TestRunTokenCommandWritesOnlyTokenToStdout(t *testing.T) {
	store := &fakeTokenStore{token: "current", rotated: "next"}
	var stdout bytes.Buffer
	if err := RunTokenCommand(context.Background(), CommandToken, store, &stdout); err != nil {
		t.Fatal(err)
	}
	if err := RunTokenCommand(context.Background(), CommandRotateToken, store, &stdout); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "current\nnext\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if store.reads != 1 || store.rotates != 1 {
		t.Fatalf("store calls = %d/%d", store.reads, store.rotates)
	}
}

func TestRunTokenCommandDoesNotInventTokenWithoutStore(t *testing.T) {
	var stdout bytes.Buffer
	err := RunTokenCommand(context.Background(), CommandToken, nil, &stdout)
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
		t.Fatalf("error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
