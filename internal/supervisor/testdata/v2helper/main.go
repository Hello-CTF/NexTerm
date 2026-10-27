//go:build unix

// Command v2helper is a protocol 2 supervisor helper for tests. It speaks the
// wire protocol of the released v1.2.8/v1.2.9 helpers (frame codec, hello
// with feature negotiation, list, create, detach, kill-session and their
// error codes) and keeps their on-disk contract: the state directory lock
// (helper.lock), helper.pid, the versioned endpoint and the session registry
// (sessions/<id>/session.json with an output.raw recording). It imports none
// of the current supervisor package, so a change there cannot make the fixture
// speak protocol 3 by accident.
//
// Flags beyond the released ones select older or misbehaving helpers:
// --no-activity answers like a protocol 2 helper from before the activity
// feature, --ignore-term keeps running after SIGTERM so only SIGKILL ends it,
// and --session <shell command> starts a PTY session as if a client had
// created it (repeatable).
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/platform"
)

const protocolVersion = 2

const (
	frameHello byte = 1 + iota
	frameHelloAck
	frameError
	frameCreate
	frameCreated
	frameList
	frameListed
	frameAttach
	frameAttached
	frameInput
	frameInputAck
	frameResize
	frameOK
	frameKill
	frameGetVersions
	frameVersions
	frameSetVersions
	frameDetach
	frameOutput
	frameExit
	frameFatal
	frameKillSession
	frameExecOutput
)

// helperFeatures is HelperFeatures() of v1.2.9.
var helperFeatures = []string{"activity", "create_attach", "exec", "input_window", "metadata", "output_credit", "output_zstd", "replay_gzip", "runtime_metadata"}

type runtimeMetadata struct {
	Version  string   `json:"version"`
	Commit   string   `json:"commit"`
	Protocol int      `json:"protocol"`
	SHA256   string   `json:"sha256"`
	Features []string `json:"features,omitempty"`
}

type activitySnapshot struct {
	PTY  int `json:"pty"`
	Exec int `json:"exec"`
}

type helloMsg struct {
	Version     int               `json:"version"`
	StateDigest string            `json:"state_digest,omitempty"`
	Features    []string          `json:"features,omitempty"`
	Runtime     *runtimeMetadata  `json:"runtime,omitempty"`
	Activity    *activitySnapshot `json:"activity,omitempty"`
}

type createMsg struct {
	ID      string   `json:"id,omitempty"`
	Command []string `json:"command,omitempty"`
	Cols    uint32   `json:"cols,omitempty"`
	Rows    uint32   `json:"rows,omitempty"`
	Pty     *bool    `json:"pty,omitempty"`
}

type killSessionMsg struct {
	ID string `json:"id"`
}

type errorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type info struct {
	ID          string     `json:"id"`
	CreatedAt   time.Time  `json:"created_at"`
	Incarnation string     `json:"incarnation"`
	Cols        uint32     `json:"cols"`
	Rows        uint32     `json:"rows"`
	Dead        bool       `json:"dead"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// registryEntry is session.json of v1.2.9.
type registryEntry struct {
	ID          string     `json:"id"`
	CreatedAt   time.Time  `json:"created_at"`
	Incarnation string     `json:"incarnation"`
	Cols        uint32     `json:"cols"`
	Rows        uint32     `json:"rows"`
	Command     []string   `json:"command,omitempty"`
	Running     bool       `json:"running"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

type session struct {
	entry registryEntry
	exec  bool
	cmd   *exec.Cmd
	done  chan struct{}
}

type helper struct {
	stateDir string
	digest   string
	runtime  runtimeMetadata
	activity bool

	mu       sync.Mutex
	sessions map[string]*session
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "supervisor-helper" {
		os.Exit(2)
	}
	var stateDir string
	var sessions []string
	activity, ignoreTerm := true, false
	for index := 2; index < len(os.Args); index++ {
		switch os.Args[index] {
		case "--probe":
			fmt.Println(protocolVersion)
			return
		case "--state-dir":
			index++
			if index < len(os.Args) {
				stateDir = os.Args[index]
			}
		case "--no-activity":
			activity = false
		case "--ignore-term":
			ignoreTerm = true
		case "--session":
			index++
			if index < len(os.Args) {
				sessions = append(sessions, os.Args[index])
			}
		}
	}
	if err := run(stateDir, activity, ignoreTerm, sessions); err != nil {
		fmt.Fprintln(os.Stderr, "supervisor-helper:", err)
		os.Exit(1)
	}
}

func run(stateDir string, activity, ignoreTerm bool, sessions []string) error {
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(stateDir, "helper.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("supervisor helper state %s is locked by another helper: %w", stateDir, err)
	}
	pidPath := filepath.Join(stateDir, "helper.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(strconv.Itoa(os.Geteuid()) + "\x00" + stateDir))
	h := &helper{stateDir: stateDir, digest: hex.EncodeToString(sum[:]), activity: activity, sessions: make(map[string]*session)}
	h.runtime = runtimeMetadata{Version: "1.2.9", Commit: "v2fixture", Protocol: protocolVersion, SHA256: executableDigest(), Features: helperFeatures}
	endpoint := endpointPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		return err
	}
	_ = os.Remove(endpoint)
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return err
	}
	for _, command := range sessions {
		if _, err := h.create(createMsg{ID: newID(), Command: []string{"/bin/sh", "-c", command}, Cols: 80, Rows: 24}, false); err != nil {
			return err
		}
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go h.accept(listener)
	for sig := range signals {
		if sig == syscall.SIGTERM && ignoreTerm {
			continue
		}
		break
	}
	_ = listener.Close()
	h.closeSessions()
	_ = os.Remove(pidPath)
	return nil
}

// endpointPath is helperEndpoint of v1.2.9 on Unix.
func endpointPath(stateDir string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", os.Geteuid(), stateDir)))
	return filepath.Join(platform.TempDir(), fmt.Sprintf("nexterm-sup-%x", sum[:8]), fmt.Sprintf("supervisor-v%d.sock", protocolVersion))
}

func executableDigest() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (h *helper) accept(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go h.serve(conn)
	}
}

func writeFrame(w io.Writer, kind byte, message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header, uint32(len(payload)))
	header[4] = kind
	_, err = w.Write(append(header, payload...))
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > 256*1024 {
		return 0, nil, errors.New("frame too large")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[4], payload, nil
}

func (h *helper) serve(conn net.Conn) {
	var execSession *session
	defer func() {
		_ = conn.Close()
		if execSession != nil {
			h.kill(execSession.entry.ID)
		}
	}()
	kind, payload, err := readFrame(conn)
	if err != nil {
		return
	}
	if kind != frameHello {
		_ = writeFrame(conn, frameError, errorMsg{Code: "protocol", Message: "expected hello frame"})
		return
	}
	var hello helloMsg
	if err := json.Unmarshal(payload, &hello); err != nil {
		_ = writeFrame(conn, frameError, errorMsg{Code: "protocol", Message: err.Error()})
		return
	}
	if hello.Version != protocolVersion {
		_ = writeFrame(conn, frameError, errorMsg{Code: "version_mismatch", Message: fmt.Sprintf("protocol version %d is not supported", hello.Version)})
		return
	}
	if hello.StateDigest != h.digest {
		_ = writeFrame(conn, frameError, errorMsg{Code: "state_mismatch", Message: "supervisor state digest does not match this endpoint"})
		return
	}
	features := slices.Clone(helperFeatures)
	if !h.activity {
		features = slices.DeleteFunc(features, func(feature string) bool { return feature == "activity" })
	}
	ack := helloMsg{Version: protocolVersion, Features: features}
	if slices.Contains(hello.Features, "runtime_metadata") {
		runtime := h.runtime
		ack.Runtime = &runtime
	}
	if h.activity && slices.Contains(hello.Features, "activity") {
		snapshot := h.snapshot()
		ack.Activity = &snapshot
	}
	if err := writeFrame(conn, frameHelloAck, ack); err != nil {
		return
	}
	for {
		kind, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		switch kind {
		case frameList:
			_ = writeFrame(conn, frameListed, h.list())
		case frameCreate:
			var msg createMsg
			if err := json.Unmarshal(payload, &msg); err != nil {
				_ = writeFrame(conn, frameError, errorMsg{Code: "protocol", Message: err.Error()})
				continue
			}
			usePTY := msg.Pty == nil || *msg.Pty
			if !usePTY && !slices.Contains(hello.Features, "exec") {
				_ = writeFrame(conn, frameError, errorMsg{Code: "protocol", Message: "client does not support non-PTY exec"})
				continue
			}
			created, err := h.create(msg, !usePTY)
			if err != nil {
				_ = writeFrame(conn, frameError, errorMsg{Code: "internal", Message: err.Error()})
				continue
			}
			if !usePTY {
				execSession = created
			}
			_ = writeFrame(conn, frameCreated, created.info())
		case frameKillSession:
			var msg killSessionMsg
			_ = json.Unmarshal(payload, &msg)
			h.kill(msg.ID)
			_ = writeFrame(conn, frameOK, struct{}{})
		case frameDetach:
			_ = writeFrame(conn, frameOK, struct{}{})
			return
		default:
			_ = writeFrame(conn, frameError, errorMsg{Code: "protocol", Message: fmt.Sprintf("unexpected frame type %d", kind)})
			return
		}
	}
}

func (s *session) info() info {
	return info{
		ID: s.entry.ID, CreatedAt: s.entry.CreatedAt, Incarnation: s.entry.Incarnation,
		Cols: s.entry.Cols, Rows: s.entry.Rows, Dead: !s.entry.Running,
		ExitCode: s.entry.ExitCode, FinishedAt: s.entry.FinishedAt,
	}
}

func (h *helper) snapshot() activitySnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	var snapshot activitySnapshot
	for _, s := range h.sessions {
		if !s.entry.Running {
			continue
		}
		if s.exec {
			snapshot.Exec++
		} else {
			snapshot.PTY++
		}
	}
	return snapshot
}

// list is listCanonical of v1.2.9: PTY sessions only, exec sessions hidden.
func (h *helper) list() []info {
	h.mu.Lock()
	defer h.mu.Unlock()
	infos := make([]info, 0, len(h.sessions))
	for _, s := range h.sessions {
		if !s.exec {
			infos = append(infos, s.info())
		}
	}
	slices.SortFunc(infos, func(a, b info) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return infos
}

func (h *helper) create(msg createMsg, execOnly bool) (*session, error) {
	if len(msg.Command) == 0 {
		return nil, errors.New("command is required")
	}
	s := &session{exec: execOnly, done: make(chan struct{}), entry: registryEntry{
		ID: msg.ID, CreatedAt: time.Now().UTC(), Incarnation: newID(),
		Cols: msg.Cols, Rows: msg.Rows, Command: msg.Command, Running: true,
	}}
	cmd := exec.Command(msg.Command[0], msg.Command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if !execOnly {
		dir := filepath.Join(h.stateDir, "sessions", msg.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		recording, err := os.OpenFile(filepath.Join(dir, "output.raw"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		defer func() { _ = recording.Close() }()
		cmd.Stdout, cmd.Stderr = recording, recording
		if err := h.writeEntry(s.entry); err != nil {
			return nil, err
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s.cmd = cmd
	h.mu.Lock()
	h.sessions[s.entry.ID] = s
	h.mu.Unlock()
	go func() {
		err := cmd.Wait()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		finished := time.Now().UTC()
		h.mu.Lock()
		s.entry.Running = false
		s.entry.ExitCode = &code
		s.entry.FinishedAt = &finished
		entry := s.entry
		h.mu.Unlock()
		if !s.exec {
			_ = h.writeEntry(entry)
		}
		close(s.done)
	}()
	return s, nil
}

// newID returns an identifier shaped like the ids package's: 26 characters.
func newID() string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var raw [26]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	for index := range raw {
		raw[index] = alphabet[int(raw[index])%len(alphabet)]
	}
	return string(raw[:])
}

func (h *helper) writeEntry(entry registryEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	path := filepath.Join(h.stateDir, "sessions", entry.ID, "session.json")
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (h *helper) kill(id string) {
	h.mu.Lock()
	s := h.sessions[id]
	h.mu.Unlock()
	if s == nil || s.cmd == nil {
		return
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	<-s.done
}

// closeSessions ends every session on shutdown, as v1.2.9 Supervisor.Close
// does; their registry entries are written as exited.
func (h *helper) closeSessions() {
	h.mu.Lock()
	sessions := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	for _, s := range sessions {
		h.kill(s.entry.ID)
	}
}
