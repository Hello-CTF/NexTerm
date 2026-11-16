package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The head keeps the command and its first output, the tail keeps where a build or log usually fails.
const (
	headShareNumerator   = 2
	headShareDenominator = 5
	// markerReserve leaves room for the omission marker inside the byte budget.
	markerReserve = 128
	// minBlockBytes and minBlockLines floor one exec command's share of the combined budget.
	minBlockBytes = 512
	minBlockLines = 8
)

// capHeadTail caps long command- or log-like output to the overall budget, keeping its beginning and its end
// around an omission marker. It returns the text for the UI, the text for the model and whether anything was cut;
// text within the budget comes back byte for byte.
func capHeadTail(text string) (string, string, bool) {
	return capHeadTailN(text, MaxOutputBytes, MaxOutputLines)
}

func capHeadTailN(text string, maxBytes, maxLines int) (string, string, bool) {
	totalLines := lineCount(text)
	if len(text) <= maxBytes && totalLines <= maxLines {
		return text, text, false
	}
	budget := max(maxBytes-markerReserve, 2)
	headBytes := budget * headShareNumerator / headShareDenominator
	tailBytes := budget - headBytes
	headLines := max(maxLines*headShareNumerator/headShareDenominator, 1)
	tailLines := max(maxLines-headLines, 1)

	// Head end: after at most headLines lines and headBytes bytes, on a line boundary when one exists.
	he := len(text)
	if idx := nthNewline(text, headLines); idx >= 0 {
		he = idx
	}
	if he > headBytes {
		he = headBytes
		if text[he] != '\n' {
			if idx := strings.LastIndexByte(text[:he], '\n'); idx >= 0 {
				he = idx
			} else {
				he = runeFloor(text, he)
			}
		}
	}
	// Tail start: at most tailLines lines and tailBytes bytes from the end, on a line boundary when one exists.
	ts := 0
	if idx := nthNewlineFromEnd(text, tailLines); idx >= 0 {
		ts = idx + 1
	}
	if len(text)-ts > tailBytes {
		ts = len(text) - tailBytes
		if text[ts-1] != '\n' {
			if idx := strings.IndexByte(text[ts:], '\n'); idx >= 0 {
				ts += idx + 1
			} else {
				ts = runeCeil(text, ts)
			}
		}
	}
	if ts < he {
		ts = he
	}
	head, tail := text[:he], text[ts:]
	omittedBytes := len(text) - len(head) - len(tail)
	omittedLines := max(totalLines-lineCount(head)-lineCount(tail), 0)
	display := fmt.Sprintf("%s\n[... 已省略 %d 行 / %d 字节 ...]\n%s", head, omittedLines, omittedBytes, tail)
	model := fmt.Sprintf("%s\n[... omitted %d lines / %d bytes ...]\n%s", head, omittedLines, omittedBytes, tail)
	return display, model, true
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

// nthNewline returns the index of the n-th newline, or -1 when the text has fewer.
func nthNewline(text string, n int) int {
	index := -1
	for i := 0; i < n; i++ {
		next := strings.IndexByte(text[index+1:], '\n')
		if next < 0 {
			return -1
		}
		index += next + 1
	}
	return index
}

// nthNewlineFromEnd returns the index of the n-th newline counting from the end, or -1.
func nthNewlineFromEnd(text string, n int) int {
	end := len(text)
	for i := 0; i < n; i++ {
		idx := strings.LastIndexByte(text[:end], '\n')
		if idx < 0 {
			return -1
		}
		end = idx
	}
	return end
}

func runeFloor(text string, end int) int {
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return end
}

func runeCeil(text string, start int) int {
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return start
}

// execBlock is one command's (or one target's) section of an exec_commands result. Its body is capped on its own
// so the structure lines around it, the header and the exit code or timeout footer, are never cut away.
type execBlock struct {
	prefix, modelPrefix string
	stdout, stderr      string
	footer, modelFooter string
}

func (b execBlock) structure() (bytes, lines int) {
	text := b.prefix + b.footer + "\n[stderr]\n"
	return len(text), strings.Count(text, "\n")
}

// renderExecBlocks caps every body to a fair share of the overall budget and joins the blocks. It also reports
// whether any stream was cut.
func renderExecBlocks(blocks []execBlock) (display, model string, cut bool) {
	byteSizes := make([]int, len(blocks))
	lineSizes := make([]int, len(blocks))
	structBytes, structLines := 0, 0
	for i, b := range blocks {
		byteSizes[i] = len(b.stdout) + len(b.stderr) + len("\n[stderr]\n")
		lineSizes[i] = lineCount(b.stdout) + lineCount(b.stderr)
		sb, sl := b.structure()
		structBytes += sb
		structLines += sl
	}
	byteShares := fairShares(byteSizes, MaxOutputBytes-structBytes, minBlockBytes)
	lineShares := fairShares(lineSizes, MaxOutputLines-structLines, minBlockLines)
	var d, m strings.Builder
	for i, b := range blocks {
		stdoutD, stdoutM, stderrD, stderrM, c := capStreams(b.stdout, b.stderr, byteShares[i], lineShares[i])
		cut = cut || c
		d.WriteString(b.prefix + stdoutD)
		m.WriteString(b.modelPrefix + stdoutM)
		if b.stderr != "" {
			d.WriteString("\n[stderr]\n" + stderrD)
			m.WriteString("\n[stderr]\n" + stderrM)
		}
		d.WriteString(b.footer)
		m.WriteString(b.modelFooter)
	}
	// Safety net for a pathological amount of structure; normally a no-op.
	display, _, cutD := capHeadTail(d.String())
	_, model, cutM := capHeadTail(m.String())
	return display, model, cut || cutD || cutM
}

func capStreams(stdout, stderr string, byteShare, lineShare int) (stdoutD, stdoutM, stderrD, stderrM string, cut bool) {
	stdoutBytes, stdoutLines := byteShare, lineShare
	stderrBytes, stderrLines := 0, 0
	if stderr != "" {
		stderrBytes = max(min(len(stderr), max(byteShare-len(stdout), byteShare/2)), 1)
		stdoutBytes = max(byteShare-stderrBytes, 1)
		stderrLines = max(min(lineCount(stderr), max(lineShare-lineCount(stdout), lineShare/2)), 1)
		stdoutLines = max(lineShare-stderrLines, 1)
	}
	var stdoutCut, stderrCut bool
	stdoutD, stdoutM, stdoutCut = capHeadTailN(stdout, stdoutBytes, stdoutLines)
	if stderr != "" {
		stderrD, stderrM, stderrCut = capHeadTailN(stderr, stderrBytes, stderrLines)
	}
	return stdoutD, stdoutM, stderrD, stderrM, stdoutCut || stderrCut
}

// fairShares splits total among the sizes: a size below its fair share keeps its size and the leftover goes to the
// larger ones. No share drops below floor.
func fairShares(sizes []int, total, floor int) []int {
	shares := make([]int, len(sizes))
	remaining := total
	pending := make([]int, len(sizes))
	for i := range sizes {
		pending[i] = i
	}
	for len(pending) > 0 {
		fair := max(remaining/len(pending), floor)
		var next []int
		settled := false
		for _, i := range pending {
			if sizes[i] <= fair {
				shares[i] = sizes[i]
				remaining -= sizes[i]
				settled = true
			} else {
				next = append(next, i)
			}
		}
		if !settled {
			for _, i := range next {
				shares[i] = fair
			}
			break
		}
		pending = next
	}
	return shares
}
