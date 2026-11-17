package session

import "regexp"

// terminalReplyPattern matches input made only of reports a terminal
// emulator writes on its own while parsing output: query replies (DA, DSR,
// CPR, window size, DECRQM, kitty keyboard flags, DCS, OSC colours) and the
// focus and colour-scheme reports. It mirrors the patterns of
// src/features/terminal/terminalResponses.ts.
var terminalReplyPattern = regexp.MustCompile(`^(?:` +
	`\x1b\[\?[0-9;]*c|\x1b\[[>=][0-9;]*c|\x1b/Z|` +
	`\x1b\[[0-9]+n|\x1b\[\?[0-9]+;[0-9]+n|` +
	`\x1b\[\??[0-9]+;[0-9]+R|` +
	`\x1b\[8;[0-9]+;[0-9]+t|` +
	`\x1b\[\??[0-9]+;[0-9]+\$y|` +
	`\x1b\[\?[0-9]+u|` +
	`\x1bP[ -~]*\x1b\\|` +
	`\x1b\](?:4|1[0-9]);(?:[0-9]+;)?(?:rgb:[0-9a-fA-F]+/[0-9a-fA-F]+/[0-9a-fA-F]+|#[0-9a-fA-F]{3,8})(?:\x07|\x1b\\)|` +
	`\x1b\[[IO]` +
	`)+$`)

// maxTerminalReplyBytes bounds the input checked as replies; a reply burst
// such as a full OSC 4 palette probe stays far below it.
const maxTerminalReplyBytes = 64 << 10

// isTerminalReply reports input that only answers the program. A view nobody
// uses still answers the queries of programs started from another device,
// and that must not count as the device being used. Shift+F3 and similar
// modified keys encode like a CPR report and are classified as replies too;
// they still reach the program.
func isTerminalReply(data []byte) bool {
	if len(data) == 0 || data[0] != 0x1b || len(data) > maxTerminalReplyBytes {
		return false
	}
	return terminalReplyPattern.Match(data)
}
