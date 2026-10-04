package hub

import (
	"context"
	"errors"
	"testing"
)

func TestHubRejectsEmptyChannelID(t *testing.T) {
	h := New(Options{})
	ctx := context.Background()
	if err := h.SendBinary(ctx, "", []byte("x")); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("send error = %v", err)
	}
	if _, err := h.Bind(""); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("bind error = %v", err)
	}
	if _, err := h.Producer(""); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("producer error = %v", err)
	}
	if ErrInvalidChannel.Error() != "通道 ID 不能为空" {
		t.Fatalf("invalid channel copy = %q", ErrInvalidChannel.Error())
	}
}
