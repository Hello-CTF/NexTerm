package account

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoginDeviceTokenBindsUsernameKeyAndExpiry(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "totp.key")
	now := time.Unix(1_700_000_000, 0).UnixMilli()
	a := New(nil, WithTOTPKeyFile(keyFile), WithNow(func() int64 { return now }))
	user := &User{ID: "user-1", Username: "Alice"}
	token, err := a.LoginDeviceToken(user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !a.LoginDeviceKnown(token, "alice") || !a.LoginDeviceKnown(token, "ALICE") {
		t.Fatal("token not accepted for its username")
	}
	restarted := New(nil, WithTOTPKeyFile(keyFile), WithNow(func() int64 { return now }))
	if !restarted.LoginDeviceKnown(token, "alice") {
		t.Fatal("token rejected after a restart with the same key file")
	}
	if a.LoginDeviceKnown(token, "bob") {
		t.Fatal("token accepted for another username")
	}
	other := New(nil, WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key")), WithNow(func() int64 { return now }))
	if other.LoginDeviceKnown(token, "alice") {
		t.Fatal("token accepted by a server with another key")
	}
	payload, signature, _ := strings.Cut(token, ".")
	if a.LoginDeviceKnown(payload+"x."+signature, "alice") || a.LoginDeviceKnown(payload, "alice") || a.LoginDeviceKnown("", "alice") {
		t.Fatal("tampered or malformed token accepted")
	}
	// The CSRF key and the device key are separate: a CSRF token is not a
	// device signature.
	csrf, err := a.CSRFToken(payload)
	if err != nil {
		t.Fatal(err)
	}
	if a.LoginDeviceKnown(payload+"."+csrf, "alice") {
		t.Fatal("CSRF token accepted as a device signature")
	}
	now += time.Hour.Milliseconds()
	if a.LoginDeviceKnown(token, "alice") {
		t.Fatal("expired token accepted")
	}
}
