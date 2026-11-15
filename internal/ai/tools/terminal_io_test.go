package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func TestEncodeKeysSendsUnknownAngleTextUnchanged(t *testing.T) {
	for _, text := range []string{
		`echo "1 < 2 > 0"`,
		`echo '<DIV>Hi</DIV>'`,
		`printf '<a-button type="primary">OK</a-button>'`,
		`cat <<EOF`,
		`x < Unknown >`,
		`<中文>`,
	} {
		encoded, err := EncodeKeys(text, false)
		if err != nil || string(encoded) != text {
			t.Fatalf("EncodeKeys(%q) = %q, %v; want the text unchanged", text, encoded, err)
		}
	}
}

func TestEncodeKeysRejectsUnsupportedKeyChords(t *testing.T) {
	for _, keys := range []string{"<ctrl-c>", "<alt+b>", "<Shift+Tab>", "<meta-x>", "<m-x>", "<s-tab>", "<f13>", "<ctrl+1>", "<alt>", "echo hi<cr>"} {
		encoded, err := EncodeKeys(keys, false)
		if err == nil {
			t.Fatalf("EncodeKeys(%q) = %q; want an error", keys, encoded)
		}
		if !strings.Contains(err.Error(), "<lt>") || !strings.Contains(err.Error(), "paste=true") {
			t.Fatalf("EncodeKeys(%q) error lacks recovery guidance: %v", keys, err)
		}
	}
}

func TestEncodeKeysSupportedNamesAreCaseInsensitive(t *testing.T) {
	encoded, err := EncodeKeys("a<Enter><TAB>< esc ><F12><C-a><Ctrl+?><LT>", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a\r\t\x1b\x1b[24~\x01\x7f<"; string(encoded) != want {
		t.Fatalf("encoded = %q, want %q", encoded, want)
	}
}

// The guard classifies send_keys text, the encoder produces the bytes: for
// text without supported key tags the two must be the same string.
func TestGuardClassifiesTheTextThatIsSent(t *testing.T) {
	for _, text := range []string{
		`echo "1 < 2 > 0"`,
		`echo '<DIV>Hi</DIV>' && rm -rf <TARGET>`,
		`sed -n '/<Item>/p' file`,
		`a<b>c<d`,
	} {
		encoded, err := EncodeKeys(text, false)
		if err != nil {
			t.Fatalf("EncodeKeys(%q): %v", text, err)
		}
		commands, controls, submits := guard.EffectiveKeyActions(text, "", -1, true)
		if !submits || len(controls) != 0 || len(commands) != 1 || commands[0] != string(encoded) {
			t.Fatalf("guard saw %q (controls %q), encoder sends %q", commands, controls, encoded)
		}
	}
}

func TestSendKeysRefusesUnsupportedChordWithoutWriting(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	registry := p1Registry(terminal)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "t"},
		p1Call("k", "send_keys", `{"keys":"<ctrl-c>"}`), nil)
	if result.OK || !strings.Contains(result.Text, "<ctrl-c>") {
		t.Fatalf("unsupported chord = %+v", result)
	}
	if len(terminal.writes) != 0 {
		t.Fatalf("unsupported chord wrote %q", terminal.writes)
	}
	literal := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "t"},
		p1Call("k2", "send_keys", `{"keys":"echo '<DIV>Hi</DIV>'"}`), nil)
	if !literal.OK || len(terminal.writes) != 1 || string(terminal.writes[0]) != `echo '<DIV>Hi</DIV>'` {
		t.Fatalf("literal angle text = %+v writes=%q", literal, terminal.writes)
	}
}

func TestExecutionRefusesUnsupportedChordBeforeConfirmation(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	execution := arbitrationExecution(terminal, guard.ReadWrite)
	output, err := execution.initial(context.Background(), p1Call("k", "send_keys", `{"keys":"rm -rf x<alt+b>","enter":true}`))
	if err != nil {
		t.Fatalf("initial asked for confirmation or failed: %v", err)
	}
	if output.OK || !strings.Contains(output.Text, "<alt+b>") || len(terminal.writes) != 0 {
		t.Fatalf("output = %+v writes=%q", output, terminal.writes)
	}
}

// burstOutput is n lines of numbered output with marker as line markerAt.
func burstOutput(n, markerAt int, marker string) string {
	var output strings.Builder
	for index := 0; index < n; index++ {
		if index == markerAt {
			output.WriteString(marker + "\r\n")
			continue
		}
		fmt.Fprintf(&output, "line %05d\r\n", index)
	}
	return output.String()
}

func TestWaitForMatchesDeepInsideLargeBurst(t *testing.T) {
	ring := newFakeRing(0, "$ make\r\n")
	anchor := ring.latest
	ring.append(burstOutput(9000, 4321, "MARKER-OK"))
	terminal := anchoredTerminal(ring)
	registry := p1Registry(terminal)
	scope := Scope{TabID: "tab"}

	single := registry.Execute(context.Background(), "job", scope,
		p1Call("w1", "wait_for", fmt.Sprintf(`{"pattern":"MARKER-OK","since_seq":%d,"timeout_ms":500}`, anchor)), nil)
	if !single.OK {
		t.Fatalf("single-tab anchored wait missed the marker: %+v", single)
	}
	combined := registry.Execute(context.Background(), "job", scope,
		p1Call("w2", "wait_for", fmt.Sprintf(`{"pattern":"MARKER-OK","lines":9000,"since_seq":%d,"timeout_ms":500}`, anchor)), nil)
	if !combined.OK || combined.Wait == nil || combined.Wait.Tabs[0].NewLines != 9000 {
		t.Fatalf("pattern+lines wait = %+v", combined)
	}
	if !strings.Contains(combined.Notes, "anchor") {
		t.Fatalf("new-output-only matching is not noted: %+v", combined)
	}
}

func TestWaitForLinesCountsLineFeeds(t *testing.T) {
	ring := newFakeRing(0, "$ ")
	anchor := ring.latest
	ring.append(burstOutput(3276, -1, "") + "partial")
	registry := p1Registry(anchoredTerminal(ring))
	scope := Scope{TabID: "tab"}

	enough := registry.Execute(context.Background(), "job", scope,
		p1Call("w1", "wait_for", fmt.Sprintf(`{"lines":3276,"since_seq":%d,"timeout_ms":500}`, anchor)), nil)
	if !enough.OK || enough.Wait.Tabs[0].NewLines != 3276 {
		t.Fatalf("3276 complete lines = %+v", enough)
	}
	// The trailing partial line has no line feed yet and is not counted.
	more := registry.Execute(context.Background(), "job", scope,
		p1Call("w2", "wait_for", fmt.Sprintf(`{"lines":3277,"since_seq":%d,"timeout_ms":50}`, anchor)), nil)
	if more.OK || more.Wait == nil || !more.Wait.TimedOut || more.Wait.Tabs[0].NewLines != 3276 {
		t.Fatalf("partial line counted: %+v", more)
	}
}

func TestSinceSeqBeyondLatestIsRejected(t *testing.T) {
	ring := newFakeRing(0, "hello world")
	registry := p1Registry(anchoredTerminal(ring))
	scope := Scope{TabID: "tab"}
	for _, call := range []Call{
		p1Call("r", "read_screen", `{"since_seq":500}`),
		p1Call("w", "wait_for", `{"pattern":"x","since_seq":500,"timeout_ms":5000}`),
		p1Call("l", "wait_for", `{"lines":1,"since_seq":500,"timeout_ms":5000}`),
	} {
		started := time.Now()
		result := registry.Execute(context.Background(), "job", scope, call, nil)
		if result.OK || !strings.Contains(result.Text, ErrInvalidToolCall.Error()) || !strings.Contains(result.Text, "since_seq=500") {
			t.Fatalf("%s since_seq beyond latest = %+v", call.Name, result)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("%s waited %v instead of rejecting the anchor", call.Name, elapsed)
		}
	}
	current := registry.Execute(context.Background(), "job", scope, p1Call("r2", "read_screen", `{"since_seq":11}`), nil)
	if !current.OK {
		t.Fatalf("anchor at latest = %+v", current)
	}
}

var sendAnchorPattern = regexp.MustCompile(`已发送（屏幕序号 ([0-9]+)）`)

func TestSendKeysAnchorPrecedesImmediateOutput(t *testing.T) {
	ring := newFakeRing(0, "$ ")
	terminal := anchoredTerminal(ring)
	terminal.onWrite = func() {
		// The program answers synchronously, before send_keys returns.
		ring.append("\r\nDONE\r\n$ ")
		terminal.mu.Lock()
		terminal.screen.Seq = ring.latest
		terminal.mu.Unlock()
	}
	registry := p1Registry(terminal)
	scope := Scope{SessionID: "s", TabID: "tab"}
	sent := registry.Execute(context.Background(), "job", scope, p1Call("k", "send_keys", `{"keys":"run","enter":true}`), nil)
	match := sendAnchorPattern.FindStringSubmatch(sent.Text)
	if !sent.OK || match == nil {
		t.Fatalf("send_keys = %+v", sent)
	}
	anchor, _ := strconv.ParseUint(match[1], 10, 64)
	waited := registry.Execute(context.Background(), "job", scope,
		p1Call("w", "wait_for", fmt.Sprintf(`{"pattern":"DONE","since_seq":%d,"timeout_ms":200}`, anchor)), nil)
	if !waited.OK {
		t.Fatalf("output produced by the write was before the anchor %d: %+v", anchor, waited)
	}
}

func TestReadScreenHistoryRejectsPagingArguments(t *testing.T) {
	ring := newFakeRing(0, "hello world")
	registry := p1Registry(anchoredTerminal(ring))
	for _, args := range []string{`{"history_lines":5,"since_seq":1}`, `{"history_lines":5,"cursor":"x"}`, `{"history_lines":5,"max_bytes":100}`} {
		result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"}, p1Call("r", "read_screen", args), nil)
		if result.OK || !strings.Contains(result.Text, "history_lines") {
			t.Fatalf("%s = %+v", args, result)
		}
	}
	if result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"}, p1Call("r", "read_screen", `{"history_lines":5}`), nil); !result.OK {
		t.Fatalf("plain history read = %+v", result)
	}
}

// ctxStateTerminal answers command state only while the caller's context is
// live, like a real terminal behind a context-aware lookup.
type ctxStateTerminal struct {
	*fakeTerminal
}

func (c ctxStateTerminal) CommandState(ctx context.Context, _ string) (CommandState, error) {
	if err := ctx.Err(); err != nil {
		return CommandState{}, err
	}
	return CommandState{Running: true, Sequence: 3}, nil
}

func TestTimedOutMultiTabWaitReportsLiveCommandState(t *testing.T) {
	ring := newFakeRing(0, "$ sleep 100\r\n")
	registry := p1Registry(ctxStateTerminal{anchoredTerminal(ring)})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		p1Call("w", "wait_for", `{"lines":1,"timeout_ms":30}`), nil)
	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("wait = %+v", result)
	}
	if !result.Wait.Tabs[0].Running {
		t.Fatalf("timed-out wait lost the running state: %+v", result.Wait.Tabs[0])
	}
}

func TestSendKeysRefusedWhileTakeoverOwnsTab(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond,
		TakeoverActive: func(tabID string) bool { return tabID == "t" }})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "t"}, p1Call("k", "send_keys", `{"keys":"ls","enter":true}`), nil)
	if result.OK || !strings.Contains(result.Text, ErrTakeoverActive.Error()) {
		t.Fatalf("send_keys during takeover = %+v", result)
	}
	execution := &Execution{JobID: "job", Registry: registry, Scope: Scope{SessionID: "s", TabID: "t"}, Permission: guard.Config{Mode: guard.ReadWrite}, Memory: guard.NewMemory()}
	output, err := execution.initial(context.Background(), p1Call("k2", "send_keys", `{"keys":"rm -rf x","enter":true}`))
	if err != nil || output.OK || !strings.Contains(output.Text, ErrTakeoverActive.Error()) {
		t.Fatalf("initial during takeover = %+v err=%v", output, err)
	}
	if len(terminal.writes) != 0 {
		t.Fatalf("writes during takeover = %q", terminal.writes)
	}
	read := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "t"}, p1Call("r", "read_screen", `{}`), nil)
	if !read.OK {
		t.Fatalf("read_screen during takeover = %+v", read)
	}
}

func showScreen(terminal *fakeTerminal, screen Screen) {
	terminal.mu.Lock()
	terminal.screen = screen
	terminal.mu.Unlock()
}

func typeKeys(registry *Registry, id, keys string) Output {
	args, _ := json.Marshal(map[string]any{"keys": keys})
	return registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "t"}, p1Call(id, "send_keys", string(args)), nil)
}

func observeScreen(t *testing.T, registry *Registry) {
	t.Helper()
	if result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "t"}, p1Call("r", "read_screen", `{}`), nil); !result.OK {
		t.Fatalf("read_screen = %+v", result)
	}
}

func TestSendKeysIgnoresChangingProgramOutput(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "Downloading 10%", CursorCol: 15, Cols: 80, Rows: 24}}
	registry := p1Registry(terminal)
	observeScreen(t, registry)
	showScreen(terminal, Screen{Text: "Downloading 50%", CursorCol: 15, Cols: 80, Rows: 24})
	if result := typeKeys(registry, "k", "y"); !result.OK {
		t.Fatalf("progress output blocked send_keys: %+v", result)
	}
}

func TestSendKeysSkipsArbitrationWhileRunningOrAltScreen(t *testing.T) {
	for _, test := range []struct {
		name    string
		running bool
		alt     bool
	}{{name: "running", running: true}, {name: "alt screen", alt: true}} {
		t.Run(test.name, func(t *testing.T) {
			terminal := &fakeTerminal{screen: Screen{Text: "user@h:~$ y", CursorCol: 11, Cols: 80, Rows: 24, AltScreen: test.alt}}
			terminal.commandState = CommandState{Running: test.running, Sequence: 1}
			registry := p1Registry(terminal)
			observeScreen(t, registry)
			showScreen(terminal, Screen{Text: "user@h:~$ yes", CursorCol: 13, Cols: 80, Rows: 24, AltScreen: test.alt})
			if result := typeKeys(registry, "k", "q"); !result.OK {
				t.Fatalf("send_keys = %+v", result)
			}
		})
	}
	// The same change at an idle shell prompt is unseen user input.
	terminal := &fakeTerminal{screen: Screen{Text: "user@h:~$", CursorCol: 10, Cols: 80, Rows: 24}}
	registry := p1Registry(terminal)
	observeScreen(t, registry)
	showScreen(terminal, Screen{Text: "user@h:~$ rm -rf x", CursorCol: 18, Cols: 80, Rows: 24})
	if result := typeKeys(registry, "k", "q"); result.OK || !strings.Contains(result.Text, ErrUserInputPending.Error()) {
		t.Fatalf("user input at an idle prompt = %+v", result)
	}
}

func TestTerminalInputCursorMeasuresWideCharacters(t *testing.T) {
	// "user@h:~$ " is 10 cells, "echo " 5, "文档" 4: the cursor before "ab"
	// sits at cell 19 but rune offset 7 of the input.
	screen := Screen{Text: "user@h:~$ echo 文档ab", CursorCol: 19, Cols: 80, Rows: 24}
	buffered, cursor := TerminalInputCursor(screen)
	if buffered != "echo 文档ab" || cursor != 7 {
		t.Fatalf("input = %q cursor %d", buffered, cursor)
	}
	if predicted, _, ok := predictedInput(buffered, cursor, []byte("X")); !ok || predicted != "echo 文档Xab" {
		t.Fatalf("predicted = %q", predicted)
	}
}

func TestSendKeysOwnWritesAtCJKPromptDoNotTripArbitration(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "user@h:~$ echo 文档ab", CursorCol: 19, Cols: 80, Rows: 24}}
	registry := p1Registry(terminal)
	observeScreen(t, registry)
	if result := typeKeys(registry, "k1", "X"); !result.OK {
		t.Fatalf("first write = %+v", result)
	}
	showScreen(terminal, Screen{Text: "user@h:~$ echo 文档Xab", CursorCol: 20, Cols: 80, Rows: 24})
	if result := typeKeys(registry, "k2", "Y"); !result.OK {
		t.Fatalf("own write at a CJK prompt tripped arbitration: %+v", result)
	}
}

func TestSendKeysOwnTrailingSpaceDoesNotTripArbitration(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "user@h:~$", CursorCol: 10, Cols: 80, Rows: 24}}
	registry := p1Registry(terminal)
	observeScreen(t, registry)
	if result := typeKeys(registry, "k1", "cd "); !result.OK {
		t.Fatalf("first write = %+v", result)
	}
	// The rendered screen drops the trailing blank; the cursor keeps it.
	showScreen(terminal, Screen{Text: "user@h:~$ cd", CursorCol: 13, Cols: 80, Rows: 24})
	if result := typeKeys(registry, "k2", "/tmp"); !result.OK {
		t.Fatalf("own trailing space tripped arbitration: %+v", result)
	}
}

func TestSessionWaitSnapshotCarriesScreenSequence(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	fixture.terminal().Feed([]byte("hello\r\n$ "))
	ctx := trustedContext()
	want, err := fixture.manager.ScreenSeqFor(ctx, fixture.tabID)
	if err != nil || want == 0 {
		t.Fatalf("screen seq = %d err=%v", want, err)
	}
	screen, err := SessionTerminal(fixture.manager).(WaitSnapshotTerminal).WaitSnapshot(ctx, fixture.tabID)
	if err != nil || screen.Seq != want {
		t.Fatalf("wait snapshot seq = %d err=%v, want %d", screen.Seq, err, want)
	}
}
