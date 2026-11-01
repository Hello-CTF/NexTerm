package execbatch

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Script directives let a script driven by a Runbook step report structure to
// the operator by printing lines to stdout. Script output is untrusted, so the
// grammar is deliberately small, every field is length bounded and the parser
// keeps O(MaxDirectiveLineBytes) state however much the script prints.
//
//	@NEXTERM:STEP <title>
//	@NEXTERM:PROGRESS <0-100>
//	@NEXTERM:INPUT <var> <prompt>[<TAB><default>]
//	@NEXTERM:SECRET <var> <prompt>
//	@NEXTERM:CONFIRM <prompt>
//	@NEXTERM:MESSAGE <info|warn|error> <text>
const (
	DirectivePrefix = "@NEXTERM:"
	// MaxDirectiveLineBytes bounds one directive line including the prefix.
	MaxDirectiveLineBytes = 1024
	// MaxDirectiveTextRunes bounds prompts, titles, messages and defaults.
	MaxDirectiveTextRunes = 200
	// MaxDirectiveVariableBytes bounds a variable name.
	MaxDirectiveVariableBytes = 64
	// MaxAnswerBytes bounds one answer written to a script's stdin.
	MaxAnswerBytes = 1024
)

type DirectiveKind string

const (
	DirectiveStep     DirectiveKind = "step"
	DirectiveProgress DirectiveKind = "progress"
	DirectiveInput    DirectiveKind = "input"
	DirectiveSecret   DirectiveKind = "secret"
	DirectiveConfirm  DirectiveKind = "confirm"
	DirectiveMessage  DirectiveKind = "message"
)

type MessageLevel string

const (
	LevelInfo  MessageLevel = "info"
	LevelWarn  MessageLevel = "warn"
	LevelError MessageLevel = "error"
)

// Directive is one parsed directive line. Only the fields of its Kind are set.
type Directive struct {
	Kind     DirectiveKind
	Text     string // step title, prompt or message text
	Progress int
	Level    MessageLevel
	Variable string
	Default  string
}

// ErrMalformedDirective wraps every parse failure of a line that carries the
// directive prefix.
var ErrMalformedDirective = errors.New("malformed script directive")

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformedDirective, fmt.Sprintf(format, args...))
}

// ParseDirective parses one line (without its terminator; a trailing CR is
// tolerated). isDirective is false when the line does not start with the
// prefix, in which case it is ordinary output.
func ParseDirective(line string) (d Directive, isDirective bool, err error) {
	line = strings.TrimSuffix(line, "\r")
	if !strings.HasPrefix(line, DirectivePrefix) {
		return Directive{}, false, nil
	}
	if len(line) > MaxDirectiveLineBytes {
		return Directive{}, true, malformed("line is longer than %d bytes", MaxDirectiveLineBytes)
	}
	body := line[len(DirectivePrefix):]
	command, rest, hasArgs := strings.Cut(body, " ")
	if !hasArgs {
		return Directive{}, true, malformed("%s needs arguments", command)
	}
	switch command {
	case "STEP":
		text, err := directiveText("title", rest, false)
		if err != nil {
			return Directive{}, true, err
		}
		return Directive{Kind: DirectiveStep, Text: text}, true, nil
	case "PROGRESS":
		value := strings.TrimSpace(rest)
		if value == "" || len(value) > 3 || strings.Trim(value, "0123456789") != "" {
			return Directive{}, true, malformed("PROGRESS needs an integer from 0 to 100")
		}
		number, _ := strconv.Atoi(value)
		if number > 100 {
			return Directive{}, true, malformed("PROGRESS needs an integer from 0 to 100")
		}
		return Directive{Kind: DirectiveProgress, Progress: number}, true, nil
	case "INPUT", "SECRET":
		kind := DirectiveInput
		if command == "SECRET" {
			kind = DirectiveSecret
		}
		variable, tail, ok := strings.Cut(rest, " ")
		if !ok || !validVariable(variable) {
			return Directive{}, true, malformed("%s needs a variable name and a prompt", command)
		}
		prompt, defaultValue, hasDefault := strings.Cut(tail, "\t")
		if hasDefault && kind == DirectiveSecret {
			return Directive{}, true, malformed("SECRET does not take a default")
		}
		text, err := directiveText("prompt", prompt, false)
		if err != nil {
			return Directive{}, true, err
		}
		defaultText := ""
		if hasDefault {
			if defaultText, err = directiveText("default", defaultValue, true); err != nil {
				return Directive{}, true, err
			}
		}
		return Directive{Kind: kind, Variable: variable, Text: text, Default: defaultText}, true, nil
	case "CONFIRM":
		text, err := directiveText("prompt", rest, false)
		if err != nil {
			return Directive{}, true, err
		}
		return Directive{Kind: DirectiveConfirm, Text: text}, true, nil
	case "MESSAGE":
		level, tail, ok := strings.Cut(rest, " ")
		switch MessageLevel(level) {
		case LevelInfo, LevelWarn, LevelError:
		default:
			return Directive{}, true, malformed("MESSAGE level must be info, warn or error")
		}
		if !ok {
			return Directive{}, true, malformed("MESSAGE needs text")
		}
		text, err := directiveText("text", tail, false)
		if err != nil {
			return Directive{}, true, err
		}
		return Directive{Kind: DirectiveMessage, Level: MessageLevel(level), Text: text}, true, nil
	default:
		return Directive{}, true, malformed("unknown directive %q", truncateRunes(command, 16))
	}
}

func validVariable(name string) bool {
	if name == "" || len(name) > MaxDirectiveVariableBytes {
		return false
	}
	for index, char := range name {
		switch {
		case char == '_' || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z'):
		case index > 0 && char >= '0' && char <= '9':
		default:
			return false
		}
	}
	return true
}

// directiveText validates plain display text: valid UTF-8, no control,
// format (bidi override) or line separator characters, bounded in runes.
func directiveText(field, value string, allowEmpty bool) (string, error) {
	if !utf8.ValidString(value) {
		return "", malformed("%s is not valid UTF-8", field)
	}
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return "", malformed("%s is empty", field)
	}
	if utf8.RuneCountInString(value) > MaxDirectiveTextRunes {
		return "", malformed("%s is longer than %d characters", field, MaxDirectiveTextRunes)
	}
	for _, char := range value {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) || unicode.Is(unicode.Zl, char) || unicode.Is(unicode.Zp, char) {
			return "", malformed("%s contains control characters", field)
		}
	}
	return value, nil
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "..."
}

// ValidAnswer reports whether value can be written to a script as one stdin line.
func ValidAnswer(value string) bool {
	return len(value) <= MaxAnswerBytes && utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n\x00")
}

// directiveFilter splits a stdout byte stream into ordinary output and
// directive lines. Ordinary bytes are forwarded as soon as the line is known
// not to be a directive, so live output is not delayed; only a line that
// starts with the prefix is buffered, up to MaxDirectiveLineBytes. Parts of a
// line may arrive split across any number of Write calls.
type directiveFilter struct {
	pass      func([]byte) error
	handle    func(Directive) error
	malformed func(reason string)

	state  filterState
	buf    []byte
	out    []byte
	failed error
}

type filterState int

const (
	filterPrefix filterState = iota // buf holds a partial prefix of the current line
	filterCollect
	filterPass // the current line is ordinary output
)

func newDirectiveFilter(pass func([]byte) error, handle func(Directive) error, malformedFn func(string)) *directiveFilter {
	return &directiveFilter{pass: pass, handle: handle, malformed: malformedFn}
}

func (f *directiveFilter) flushOut() error {
	if len(f.out) == 0 || f.failed != nil {
		f.out = f.out[:0]
		return f.failed
	}
	chunk := make([]byte, len(f.out))
	copy(chunk, f.out)
	f.out = f.out[:0]
	f.failed = f.pass(chunk)
	return f.failed
}

// Write consumes one chunk. An error from the pass or handle callbacks stops
// further processing and is returned by every later call.
func (f *directiveFilter) Write(p []byte) error {
	for _, b := range p {
		if f.failed != nil {
			return f.failed
		}
		switch f.state {
		case filterPass:
			f.out = append(f.out, b)
			if b == '\n' {
				f.state = filterPrefix
			}
		case filterPrefix:
			if len(f.buf) < len(DirectivePrefix) && b == DirectivePrefix[len(f.buf)] {
				f.buf = append(f.buf, b)
				if len(f.buf) == len(DirectivePrefix) {
					f.state = filterCollect
				}
				continue
			}
			f.out = append(f.out, f.buf...)
			f.out = append(f.out, b)
			f.buf = f.buf[:0]
			if b != '\n' {
				f.state = filterPass
			}
		case filterCollect:
			if b == '\n' {
				if err := f.finishLine(true); err != nil {
					return err
				}
				continue
			}
			if len(f.buf) >= MaxDirectiveLineBytes {
				f.out = append(f.out, f.buf...)
				f.out = append(f.out, b)
				f.buf = f.buf[:0]
				f.state = filterPass
				f.malformed(fmt.Sprintf("directive line is longer than %d bytes", MaxDirectiveLineBytes))
				continue
			}
			f.buf = append(f.buf, b)
		}
	}
	return f.flushOut()
}

// finishLine handles a complete collected line. terminated is false for a
// final line without a newline at end of stream.
func (f *directiveFilter) finishLine(terminated bool) error {
	line := string(f.buf)
	f.buf = f.buf[:0]
	f.state = filterPrefix
	directive, _, err := ParseDirective(line)
	if err != nil {
		f.out = append(f.out, line...)
		if terminated {
			f.out = append(f.out, '\n')
		}
		f.malformed(strings.TrimPrefix(err.Error(), ErrMalformedDirective.Error()+": "))
		return nil
	}
	if err := f.flushOut(); err != nil {
		return err
	}
	f.failed = f.handle(directive)
	return f.failed
}

// Close flushes buffered bytes at end of stream.
func (f *directiveFilter) Close() error {
	if f.failed != nil {
		return f.failed
	}
	switch f.state {
	case filterPrefix:
		f.out = append(f.out, f.buf...)
		f.buf = f.buf[:0]
	case filterCollect:
		if err := f.finishLine(false); err != nil {
			return err
		}
	}
	return f.flushOut()
}
