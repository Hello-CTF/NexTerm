//go:build linux && !android

package production

import (
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestClipboardImageCommandFollowsTheSession(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	installed := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, candidate := range names {
				if candidate == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	both := env(map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"})
	if argv, err := clipboardImageCommand(both, installed("wl-copy", "xclip")); err != nil || argv[0] != "/usr/bin/wl-copy" {
		t.Fatalf("wayland = %v, %v", argv, err)
	}
	// XWayland sessions without wl-copy fall back to xclip.
	if argv, err := clipboardImageCommand(both, installed("xclip")); err != nil || argv[0] != "/usr/bin/xclip" {
		t.Fatalf("xwayland = %v, %v", argv, err)
	}
	var ipcErr *ipc.Error
	if _, err := clipboardImageCommand(env(map[string]string{"DISPLAY": ":0"}), installed("wl-copy")); !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeUnsupported {
		t.Fatalf("x11 without xclip = %v", err)
	}
	if _, err := clipboardImageCommand(env(nil), installed("wl-copy", "xclip")); !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeUnsupported {
		t.Fatalf("no display = %v", err)
	}
}
