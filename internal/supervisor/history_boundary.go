package supervisor

// Escape parser states shared by the boundary scan and the mode scan.
const (
	scanGround byte = iota
	scanEsc
	scanCSI
	scanString
	scanStringEsc
)

// safeReplayStart returns the index in data where a replay that must not
// begin mid-sequence can start. data begins at an arbitrary offset, so the
// parser state there is unknown: a string terminator (ESC \) seen outside a
// string proves the scan began inside an OSC/DCS string and discards every
// candidate before it. The first line start outside any escape sequence wins;
// without one, the first character boundary outside any escape sequence is
// used; without either, len(data).
func safeReplayStart(data []byte) int {
	state := scanGround
	line, char := -1, -1
	for index, b := range data {
		if state == scanGround && char < 0 && (b < 0x80 || b >= 0xc0) && b != 0x1b {
			char = index
		}
		switch state {
		case scanGround:
			switch b {
			case 0x1b:
				state = scanEsc
			case '\n':
				if line < 0 {
					line = index + 1
				}
			}
		case scanEsc:
			switch {
			case b == '\\':
				// A lone string terminator: the scan started inside a string.
				line, char = -1, -1
				state = scanGround
			case b == '[':
				state = scanCSI
			case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
				state = scanString
			case b == 0x1b:
			case b >= 0x20 && b <= 0x2f:
			default:
				state = scanGround
			}
		case scanCSI:
			switch {
			case b == 0x1b:
				state = scanEsc
			case b == 0x18 || b == 0x1a:
				state = scanGround
			case b >= 0x40 && b <= 0x7e:
				state = scanGround
			}
		case scanString:
			switch b {
			case 0x1b:
				state = scanStringEsc
			case 0x07, 0x18, 0x1a:
				state = scanGround
			}
		case scanStringEsc:
			if b == '\\' {
				state = scanGround
			} else {
				state = scanString
			}
		}
	}
	if line >= 0 {
		return line
	}
	if char >= 0 {
		return char
	}
	return len(data)
}
