package supervisor

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/applog"
	"github.com/Hello-CTF/NexTerm/internal/shellenv"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

// helperLogName is the helper's diagnostic log inside the state directory.
// The file is also the helper's stdout and stderr, so panics and startup
// errors land beside the structured lines.
const helperLogName = "helper.log"

// helperLogMaxBytes bounds one log file. With the single rollover file
// (helper.log.1) the helper never keeps more than 1 MiB of log.
const helperLogMaxBytes = 512 << 10

// helperLog appends one line per lifecycle event, in the form
//
//	2026-01-02T15:04:05.000Z pid=123 event=session_create id=... kind=pty ...
//
// Values containing spaces, quotes or control characters are quoted and every
// value passes through applog.RedactText. Terminal content is never logged:
// callers pass identifiers, paths, exit codes and error text only. A nil
// *helperLog discards everything, and a write failure is ignored because
// diagnostics must never affect a session.
type helperLog struct {
	path string
	now  func() time.Time

	mu   sync.Mutex
	file *os.File
	size int64
}

func newHelperLog(stateDir string) *helperLog {
	return &helperLog{path: filepath.Join(stateDir, helperLogName), now: time.Now}
}

func (l *helperLog) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// event writes one line; pairs are key, value, key, value, ...
func (l *helperLog) event(name string, pairs ...any) {
	if l == nil {
		return
	}
	var line strings.Builder
	line.WriteString(l.now().UTC().Format("2006-01-02T15:04:05.000Z"))
	line.WriteString(" pid=")
	line.WriteString(strconv.Itoa(os.Getpid()))
	line.WriteString(" event=")
	line.WriteString(name)
	for index := 0; index+1 < len(pairs); index += 2 {
		line.WriteByte(' ')
		line.WriteString(logToken(pairs[index]))
		line.WriteByte('=')
		line.WriteString(logValue(pairs[index+1]))
	}
	line.WriteByte('\n')
	l.write([]byte(line.String()))
}

func (l *helperLog) write(data []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return
		}
		l.file, l.size = file, info.Size()
	}
	if l.size+int64(len(data)) > helperLogMaxBytes && l.size > 0 {
		_ = l.file.Close()
		l.file = nil
		_ = os.Rename(l.path, l.path+".1")
		file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		l.file, l.size = file, 0
	}
	n, _ := l.file.Write(data)
	l.size += int64(n)
}

func logToken(value any) string {
	text, _ := value.(string)
	if text == "" {
		return "key"
	}
	return text
}

func logValue(value any) string {
	var text string
	switch typed := value.(type) {
	case string:
		text = typed
	case error:
		if typed != nil {
			text = typed.Error()
		}
	case int:
		text = strconv.Itoa(typed)
	case bool:
		if typed {
			text = "yes"
		} else {
			text = "no"
		}
	case time.Time:
		text = typed.UTC().Format(time.RFC3339Nano)
	case interface{ String() string }:
		text = typed.String()
	default:
		text = anyString(value)
	}
	text = applog.RedactText(text)
	if text == "" {
		return `""`
	}
	if strings.ContainsFunc(text, func(r rune) bool { return r <= ' ' || r == '"' || r == '=' || r == 0x7f }) {
		return strconv.Quote(text)
	}
	return text
}

func anyString(value any) string {
	switch typed := value.(type) {
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case *int:
		if typed != nil {
			return strconv.Itoa(*typed)
		}
	}
	return ""
}

// logStart records what the helper is and the environment it inherited.
func (l *helperLog) logStart(stateDir string) {
	executable, _ := os.Executable()
	l.event("start",
		"version", version.Version, "commit", version.Commit, "protocol", ProtocolVersion,
		"exe", executable, "start", l.now(), "go", runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH,
		"state_dir", stateDir,
		"env_shell", os.Getenv("SHELL"), "env_path", os.Getenv("PATH"))
}

// logCreate records a new session without its command arguments beyond the
// program: the shell chosen, where the resolver says it came from, and where
// the PATH came from.
func (l *helperLog) logCreate(id string, usePTY bool, command []string, dir string) {
	if l == nil {
		return
	}
	status := shellenv.Default().Status()
	program := ""
	if len(command) > 0 {
		program = command[0]
	}
	kind := "exec"
	if usePTY {
		kind = "pty"
	}
	l.event("session_create", "id", id, "kind", kind, "shell", program,
		"resolver_shell", status.Shell, "shell_source", string(status.ShellSource),
		"path_source", status.PathSource, "env_method", status.Method,
		"cwd", dir, "durable", usePTY)
}

func (l *helperLog) logExit(id string, code *int, signal string) {
	if l == nil {
		return
	}
	exit := "none"
	if code != nil {
		exit = strconv.Itoa(*code)
	}
	l.event("session_exit", "id", id, "exit_code", exit, "signal", signal)
}

// logHelperRequest records a stop or replace request made by a client process
// against the helper of stateDir, with the consent fields it covered.
func logHelperRequest(stateDir, name string, pairs ...any) {
	l := newHelperLog(stateDir)
	defer l.close()
	l.event(name, pairs...)
}
