package guard

import "testing"

func TestR3RawControlSubmitAndBackspace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		keys     string
		buffered string
		risk     Risk
	}{
		{name: "ctrl o submits", keys: "<ctrl+o>", buffered: "rm -rf /home/user", risk: Forbidden},
		{name: "raw ctrl o submits", keys: "\x0f", buffered: "rm -rf /home/user", risk: Forbidden},
		{name: "raw ctrl h backspaces", keys: "\b<enter>", buffered: "rm -rf /homeX", risk: Forbidden},
		{name: "raw history up", keys: "\x1b[A<enter>", buffered: "ls", risk: Danger},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifySendKeysWithBuffer(test.keys, test.buffered, false, nil); got.Risk != test.risk {
				t.Fatalf("ClassifySendKeysWithBuffer(%q, %q) = %v, want %s", test.keys, test.buffered, got, test.risk)
			}
		})
	}
}

func TestR3CtrlOAllowsSafeSubmission(t *testing.T) {
	t.Parallel()
	if got := ClassifySendKeysWithBuffer("<ctrl+o>", "ls", false, nil); got.Risk != Safe {
		t.Fatalf("safe ctrl+o submission = %v", got)
	}
}
