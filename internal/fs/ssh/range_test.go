package ssh

import (
	"context"
	"strings"
	"testing"
)

func TestSFTPReadRange(t *testing.T) {
	filesystem, _ := newTestFS(t, nil)
	ctx := context.Background()
	content := strings.Repeat("0123456789", 10)
	if err := filesystem.WriteFile(ctx, "app.log", []byte(content), false); err != nil {
		t.Fatal(err)
	}
	data, size, err := filesystem.ReadRange(ctx, "app.log", 0, 25)
	if err != nil || size != 100 || string(data) != content[:25] {
		t.Fatalf("first chunk = %q, size %d, %v", data, size, err)
	}
	data, size, err = filesystem.ReadRange(ctx, "app.log", 90, 64)
	if err != nil || size != 100 || string(data) != content[90:] {
		t.Fatalf("tail chunk = %q, size %d, %v", data, size, err)
	}
	data, size, err = filesystem.ReadRange(ctx, "app.log", 100, 64)
	if err != nil || size != 100 || len(data) != 0 {
		t.Fatalf("offset at size = %q, size %d, %v", data, size, err)
	}
	data, _, err = filesystem.ReadRange(ctx, "app.log", 1000, 64)
	if err != nil || len(data) != 0 {
		t.Fatalf("offset beyond size = %q, %v", data, err)
	}
	if _, _, err := filesystem.ReadRange(ctx, "app.log", -1, 64); err == nil {
		t.Fatal("negative offset succeeded")
	}
	if _, _, err := filesystem.ReadRange(ctx, "app.log", 0, 0); err == nil {
		t.Fatal("zero maxBytes succeeded")
	}
	if _, _, err := filesystem.ReadRange(ctx, "missing.log", 0, 64); err == nil {
		t.Fatal("missing file succeeded")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := filesystem.ReadRange(cancelled, "app.log", 0, 64); err == nil {
		t.Fatal("cancelled context succeeded")
	}
}
