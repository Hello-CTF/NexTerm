package guard

import "testing"

func TestR1SendKeysChecksBufferedSubmissionAndRawControls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		keys     string
		buffered string
		enter    bool
		risk     Risk
	}{
		{name: "buffered write", keys: "; ls", buffered: "rm -rf /tmp/victim", enter: true, risk: NeedsConfirm},
		{name: "buffered critical write", keys: "; ls", buffered: "rm -rf /home/user", enter: true, risk: Forbidden},
		{name: "backspace reveals critical path", keys: "<backspace><enter>", buffered: "rm -rf /homeX", risk: Forbidden},
		{name: "cursor edit reveals critical path", keys: "<left><delete><enter>", buffered: "rm -rf /homeX", risk: Forbidden},
		{name: "history recall is unverifiable", keys: "<up><enter>", buffered: "ls", risk: Danger},
		{name: "tab completion is unverifiable", keys: "<tab><enter>", buffered: "ls", risk: Danger},
		{name: "raw tab completion is unverifiable", keys: "\t", buffered: "ls", risk: Danger},
		{name: "padded history recall is unverifiable", keys: "< up ><enter>", buffered: "ls", risk: Danger},
		{name: "raw ctrl c", keys: "\x03", risk: NeedsConfirm},
		{name: "raw escape", keys: "\x1b", risk: NeedsConfirm},
		{name: "safe empty buffer", keys: "ls", enter: true, risk: Safe},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifySendKeysWithBuffer(test.keys, test.buffered, test.enter, nil); got.Risk != test.risk {
				t.Fatalf("ClassifySendKeysWithBuffer(%q, %q) = %v, want %s", test.keys, test.buffered, got, test.risk)
			}
		})
	}
}
