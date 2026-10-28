//go:build unix

package supervisor

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// manyLines prints about 6.9 MB of numbered lines and exits.
const manyLines = `i=0; while [ $i -lt 6 ]; do seq -f 'line-%07g' 1 150000; i=$((i+1)); done; printf 'end-marker\n'`

func historySession(t *testing.T, supervisor *Supervisor, limit uint64, script string) *Session {
	t.Helper()
	session, err := supervisor.Create(context.Background(), CreateOptions{
		ID:           ids.New(),
		Command:      []string{"/bin/sh", "-c", script},
		Env:          []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:         80,
		Rows:         24,
		HistoryLimit: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func waitDead(t *testing.T, session *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = session.Wait(ctx)
	if !session.Info().Dead {
		t.Fatal("session did not exit")
	}
}

func TestSupervisorHistoryLimitBoundsDiskAndTruncatesAttach(t *testing.T) {
	supervisor := testSupervisor(t)
	session := historySession(t, supervisor, MinHistoryLimit, manyLines)
	waitDead(t, session)

	info := session.Info()
	if !info.HistoryTruncated || info.HistoryBase == 0 || info.HistoryLimit != MinHistoryLimit {
		t.Fatalf("info = %+v, want truncated history with the requested limit", info)
	}
	if info.HistoryNext < 6*1024*1024 {
		t.Fatalf("history next = %d, want every recorded byte counted", info.HistoryNext)
	}
	if disk := segmentBytesOnDisk(t, sessionDir(supervisor.StateDir(), session.ID())); disk > MinHistoryLimit || disk != info.HistoryNext-info.HistoryBase {
		t.Fatalf("segments hold %d bytes for a %d byte limit (retained %d)", disk, MinHistoryLimit, info.HistoryNext-info.HistoryBase)
	}
	recording, err := readRecording(supervisor.StateDir(), session.ID())
	if err != nil {
		t.Fatal(err)
	}

	attachment, err := supervisor.AttachFrom(context.Background(), session.ID(), 10)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Detach() }()
	replay := attachment.Replay()
	if !replay.Truncated || replay.ReplayFrom < info.HistoryBase || replay.HistoryBase != info.HistoryBase {
		t.Fatalf("attach below the base = %+v, want truncated resume after base %d", replay, info.HistoryBase)
	}
	start := replay.ReplayFrom - info.HistoryBase
	if start > 0 && recording[start-1] != '\n' {
		t.Fatalf("truncated resume starts mid-line: %q", recording[start-1:start+16])
	}
	if got := readToEOF(t, attachment); !bytes.Equal(got, recording[start:]) {
		t.Fatal("truncated resume does not continue at its reported offset")
	}

	inside := info.HistoryBase + 1000
	exact, err := supervisor.AttachFrom(context.Background(), session.ID(), inside)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exact.Detach() }()
	if replay := exact.Replay(); replay.Truncated || replay.ReplayFrom != inside {
		t.Fatalf("attach inside the history = %+v, want an exact resume", replay)
	}
	if got := readToEOF(t, exact); !bytes.Equal(got, recording[1000:]) {
		t.Fatal("exact resume differs from the recording")
	}
}

func TestIPCAttachReportsTruncationAndTail(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()
	session := historySession(t, supervisor, MinHistoryLimit, manyLines)
	waitDead(t, session)
	info := session.Info()
	recording, err := readRecording(supervisor.StateDir(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	identity := session.Identity()

	stream, err := client.AttachFrom(ctx, session.ID(), &identity, 0)
	if err != nil {
		t.Fatal(err)
	}
	replay := stream.Replay()
	if !replay.Truncated || replay.ReplayFrom < info.HistoryBase {
		t.Fatalf("wire attach below the base = %+v", replay)
	}
	if got := readToEOF(t, stream); !bytes.Equal(got, recording[replay.ReplayFrom-info.HistoryBase:]) {
		t.Fatal("wire truncated resume differs from the recording")
	}
	if consumed := stream.ConsumedOffset(); consumed != info.HistoryNext {
		t.Fatalf("consumed offset = %d, want %d", consumed, info.HistoryNext)
	}
	_ = stream.Close()

	tail, err := client.AttachWithOptions(ctx, session.ID(), &identity, AttachOptions{Tail: 4096})
	if err != nil {
		t.Fatal(err)
	}
	replay = tail.Replay()
	if replay.Truncated || replay.DisplayFrom != replay.ReplayFrom || replay.DisplayFrom < info.HistoryNext-4096 {
		t.Fatalf("tail attach = %+v", replay)
	}
	if recording[replay.DisplayFrom-info.HistoryBase-1] != '\n' {
		t.Fatal("tail does not start at a line boundary")
	}
	if got := readToEOF(t, tail); !bytes.Equal(got, recording[replay.DisplayFrom-info.HistoryBase:]) || !bytes.HasSuffix(got, []byte("end-marker\r\n")) {
		t.Fatalf("tail replay = %q", got)
	}
	_ = tail.Close()

	retainFrom := info.HistoryNext - 20000
	retained, err := client.AttachWithOptions(ctx, session.ID(), &identity, AttachOptions{Tail: 4096, Offset: retainFrom, Retain: true})
	if err != nil {
		t.Fatal(err)
	}
	replay = retained.Replay()
	if replay.ReplayFrom != retainFrom || replay.DisplayFrom < info.HistoryNext-4096 || replay.Truncated {
		t.Fatalf("retained tail attach = %+v", replay)
	}
	if got := readToEOF(t, retained); !bytes.Equal(got, recording[retainFrom-info.HistoryBase:]) {
		t.Fatal("retained tail does not stream from the retained offset")
	}
	_ = retained.Close()

	lost, err := client.AttachWithOptions(ctx, session.ID(), &identity, AttachOptions{Tail: 4096, Offset: 5, Retain: true})
	if err != nil {
		t.Fatal(err)
	}
	if replay := lost.Replay(); !replay.Truncated || replay.ReplayFrom != info.HistoryBase {
		t.Fatalf("retain below the base = %+v, want truncated from the base", replay)
	}
	_ = lost.Close()

	// A retained offset beyond the recording (a transcript that ran ahead of
	// output lost in a host crash) is clamped instead of failing the attach.
	ahead, err := client.AttachWithOptions(ctx, session.ID(), &identity, AttachOptions{Tail: 4096, Offset: info.HistoryNext + 5000, Retain: true})
	if err != nil {
		t.Fatalf("retain beyond the recording = %v", err)
	}
	if replay := ahead.Replay(); replay.Truncated || replay.ReplayFrom != replay.DisplayFrom || replay.HistoryNext != info.HistoryNext {
		t.Fatalf("retain beyond the recording = %+v, want a plain tail", replay)
	}
	_ = ahead.Close()

	if _, err := client.AttachWithOptions(ctx, session.ID(), &identity, AttachOptions{Offset: info.HistoryNext + 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("attach beyond the recording = %v", err)
	}
}

func TestIPCReadHistoryIsBoundedAndSafe(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()
	session := historySession(t, supervisor, MinHistoryLimit, manyLines)
	waitDead(t, session)
	info := session.Info()
	recording, err := readRecording(supervisor.StateDir(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	identity := session.Identity()

	chunk, err := client.ReadHistory(ctx, session.ID(), identity, info.HistoryNext, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk.Data) > MaxHistoryRead || chunk.End != info.HistoryNext || chunk.Truncated {
		t.Fatalf("read = %d bytes [%d, %d) truncated=%v", len(chunk.Data), chunk.Start, chunk.End, chunk.Truncated)
	}
	if recording[chunk.Start-info.HistoryBase-1] != '\n' || !bytes.Equal(chunk.Data, recording[chunk.Start-info.HistoryBase:]) {
		t.Fatal("history read does not start at a line boundary of the recording")
	}

	// Walking backwards reaches the base without gaps or overlaps.
	before := chunk.Start
	var earlier [][]byte
	for {
		next, err := client.ReadHistory(ctx, session.ID(), identity, before, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		if next.End != before {
			t.Fatalf("read ends at %d, want %d", next.End, before)
		}
		earlier = append([][]byte{next.Data}, earlier...)
		if next.Truncated {
			// The partial line at the base is dropped with the output before it.
			if next.Start < info.HistoryBase || next.Start > info.HistoryBase+256 || recording[next.Start-info.HistoryBase-1] != '\n' {
				t.Fatalf("truncated read starts at %d, base %d", next.Start, info.HistoryBase)
			}
			recording = recording[next.Start-info.HistoryBase:]
			break
		}
		if next.Start == before {
			t.Fatal("history read made no progress")
		}
		before = next.Start
	}
	walked := append(bytes.Join(earlier, nil), chunk.Data...)
	if !bytes.Equal(walked, recording) {
		t.Fatal("walking the history backwards does not reproduce the recording")
	}

	if empty, err := client.ReadHistory(ctx, session.ID(), identity, info.HistoryBase-1, 100); err != nil || len(empty.Data) != 0 || !empty.Truncated {
		t.Fatalf("read before the base = %+v, %v", empty, err)
	}
	if _, err := client.ReadHistory(ctx, session.ID(), identity, info.HistoryNext+1, 100); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("read beyond the recording = %v", err)
	}
	wrong := identity
	wrong.Incarnation = ids.New()
	if _, err := client.ReadHistory(ctx, session.ID(), wrong, info.HistoryNext, 100); !errors.Is(err, ErrIdentity) {
		t.Fatalf("read with another identity = %v", err)
	}
}

// A slow consumer whose position the history limit drops jumps to the next
// safe boundary and sees the gap, with credit kept aligned.
func TestIPCStreamCrossesHistoryGap(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()
	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:           id,
		Command:      []string{"/bin/sh", "-c", `read go; ` + manyLines},
		Env:          []string{"TERM=xterm-256color", "LC_ALL=C"},
		HistoryLimit: MinHistoryLimit,
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := stream.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	session, err := supervisor.resolveSession(id)
	if err != nil {
		t.Fatal(err)
	}
	waitDead(t, session)
	info := session.Info()
	recording, err := readRecording(supervisor.StateDir(), id)
	if err != nil {
		t.Fatal(err)
	}
	got := readToEOF(t, stream)
	if stream.Gaps() == 0 {
		t.Fatal("stream did not report the dropped output")
	}
	if consumed := stream.ConsumedOffset(); consumed != info.HistoryNext {
		t.Fatalf("consumed offset = %d, want %d", consumed, info.HistoryNext)
	}
	if suffix := recording[len(recording)-64<<10:]; !bytes.HasSuffix(got, suffix) {
		t.Fatal("output after the gap differs from the recording")
	}
	if !bytes.HasSuffix(got, []byte("end-marker\r\n")) {
		t.Fatal("stream lost the output after the gap")
	}
}

func TestEmbeddedAttachmentCrossesHistoryGap(t *testing.T) {
	supervisor := testSupervisor(t)
	session := historySession(t, supervisor, MinHistoryLimit, `read go; `+manyLines)
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Detach() }()
	if _, err := attachment.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	waitDead(t, session)
	info := session.Info()
	got := readToEOF(t, attachment)
	if attachment.Gaps() == 0 {
		t.Fatal("attachment did not report the dropped output")
	}
	if attachment.NextSeq() != info.HistoryNext || !bytes.HasSuffix(got, []byte("end-marker\r\n")) {
		t.Fatalf("attachment ended at %d of %d", attachment.NextSeq(), info.HistoryNext)
	}
}

func TestSupervisorRestartKeepsHistoryBounds(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	first, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	session := historySession(t, first, MinHistoryLimit, manyLines)
	waitDead(t, session)
	before := session.Info()
	id := session.ID()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	recovered, err := second.resolveSession(id)
	if err != nil {
		t.Fatal(err)
	}
	after := recovered.Info()
	if after.HistoryBase != before.HistoryBase || after.HistoryNext != before.HistoryNext || after.HistoryLimit != MinHistoryLimit {
		t.Fatalf("recovered history %d..%d (limit %d), want %d..%d", after.HistoryBase, after.HistoryNext, after.HistoryLimit, before.HistoryBase, before.HistoryNext)
	}
	attachment, err := second.Attach(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Detach() }()
	if got := readToEOF(t, attachment); !bytes.HasSuffix(got, []byte("end-marker\r\n")) {
		t.Fatal("recovered history is not replayable")
	}
}

// A tail replay that starts on a safe boundary, with the prelude, renders the
// same screen as replaying everything.
func TestTailReplayRendersLikeFullReplay(t *testing.T) {
	cases := map[string]string{
		"shell":       `i=0; while [ $i -lt 9000 ]; do printf '\033[1;3%dmline\033[0m %05d 漢字 ünïcode \033]0;title %d\007\n' $((i % 7)) $i $i; i=$((i+1)); done`,
		"full-screen": `printf '\033[?1049h\033[?1h\033[?2004h'; i=0; while [ $i -lt 2500 ]; do printf '\033[H\033[2J'; r=1; while [ $r -le 23 ]; do printf '\033[%d;1Hframe %05d row %02d 文字' $r $i $r; r=$((r+1)); done; i=$((i+1)); done`,
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			supervisor := testSupervisor(t)
			session := historySession(t, supervisor, DefaultHistoryLimit, script)
			waitDead(t, session)
			info := session.Info()
			recording, err := readRecording(supervisor.StateDir(), session.ID())
			if err != nil {
				t.Fatal(err)
			}
			if info.HistoryNext <= DefaultTailBytes {
				t.Fatalf("recording of %d bytes is too short for a tail", info.HistoryNext)
			}
			attachment, err := supervisor.AttachWithOptions(context.Background(), session.ID(), AttachOptions{Tail: DefaultTailBytes})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = attachment.Detach() }()
			replay := attachment.Replay()
			tail := readToEOF(t, attachment)
			if replay.DisplayFrom == 0 || uint64(len(tail)) > DefaultTailBytes {
				t.Fatalf("tail replay of %d bytes from %d", len(tail), replay.DisplayFrom)
			}

			full := terminal.NewTab("full", "s", 80, 24, terminal.UTF8)
			full.Feed(recording)
			partial := terminal.NewTab("tail", "s", 80, 24, terminal.UTF8)
			partial.Feed(replay.Prelude)
			partial.Feed(tail)
			want, got := full.Snapshot(), partial.Snapshot()
			if strings.Join(got.Lines, "\n") != strings.Join(want.Lines, "\n") {
				t.Fatalf("tail screen differs from full replay:\n got: %q\nwant: %q", got.Lines, want.Lines)
			}
			if got.AltScreen != want.AltScreen {
				t.Fatalf("tail alternate screen = %v, full replay %v", got.AltScreen, want.AltScreen)
			}
		})
	}
}

// A full-screen program that entered the alternate screen long before the
// history base still gets its tail drawn on the alternate screen.
func TestTailReplayKeepsModesSetBeforeTheBase(t *testing.T) {
	supervisor := testSupervisor(t)
	script := `printf '\033[?1049h\033[?1h'; i=0; while [ $i -lt 9000 ]; do printf '\033[H\033[2J'; r=1; while [ $r -le 23 ]; do printf '\033[%d;1Hframe %05d row %02d' $r $i $r; r=$((r+1)); done; i=$((i+1)); done`
	session := historySession(t, supervisor, MinHistoryLimit, script)
	waitDead(t, session)
	if info := session.Info(); info.HistoryBase == 0 {
		t.Fatal("the program's mode changes were not evicted")
	}
	attachment, err := supervisor.AttachWithOptions(context.Background(), session.ID(), AttachOptions{Tail: DefaultTailBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Detach() }()
	replay := attachment.Replay()
	if !bytes.Contains(replay.Prelude, []byte("\x1b[?1049h")) || !bytes.Contains(replay.Prelude, []byte("\x1b[?1h")) {
		t.Fatalf("prelude %q lost modes set before the base", replay.Prelude)
	}
	screen := terminal.NewTab("tail", "s", 80, 24, terminal.UTF8)
	screen.Feed(replay.Prelude)
	screen.Feed(readToEOF(t, attachment))
	snapshot := screen.Snapshot()
	if !snapshot.AltScreen || !strings.HasPrefix(snapshot.Lines[22], "frame 08999 row 23") {
		t.Fatalf("tail screen alt=%v last row %q", snapshot.AltScreen, snapshot.Lines[22])
	}
}

// A PTY resize resets the recorded scroll region, so a later fresh open from a
// window of another size is not confined to the old region.
func TestSessionResizeResetsRecordedScrollRegion(t *testing.T) {
	supervisor := testSupervisor(t)
	session := historySession(t, supervisor, DefaultHistoryLimit, `printf '\033[?1049h\033[2;20rregion-set\n'; read line; printf 'after-resize\n'; sleep 30`)
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Detach() }()
	readUntil(t, attachment, "region-set")
	if err := session.Resize(context.Background(), 120, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "after-resize")
	// A tail of just the last line starts after the resize.
	tail, err := supervisor.AttachWithOptions(context.Background(), session.ID(), AttachOptions{Tail: uint64(len("after-resize\r\n"))})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tail.Detach() }()
	if replay := tail.Replay(); replay.DisplayFrom == 0 {
		t.Fatalf("tail starts at %+v, want after the resize", replay)
	}
	prelude := tail.Replay().Prelude
	if !bytes.Contains(prelude, []byte("\x1b[?1049h")) || bytes.Contains(prelude, []byte("r")) {
		t.Fatalf("fresh-open prelude after a resize = %q", prelude)
	}
	if err := supervisor.Kill(context.Background(), session.ID()); err != nil {
		t.Fatal(err)
	}
}
