//go:build windows

package pty

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSessionWindowsNaturalExitEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := Start(ctx, Config{
		Path: "powershell.exe",
		Args: []string{"-NoLogo", "-NoProfile", "-Command", "[Console]::Out.Write('pty-ok'); exit 0"},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "pty-ok") {
		t.Fatalf("output = %q", output)
	}
	if err := session.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionWindowsCloseWithoutReader(t *testing.T) {
	session := startWindowsTestSession(t, context.Background(), "while ($true) { [Console]::Out.Write('x' * 65536); Start-Sleep -Milliseconds 50 }")
	closeStarted := false
	defer func() {
		if !closeStarted {
			_ = session.Close()
		}
	}()
	closeStarted = true
	closeWindowsSessionWithin(t, session)
}

func TestSessionWindowsBackpressureAndDrain(t *testing.T) {
	payload, script := windowsPayloadFixture(t, 256*1024+137)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session := startWindowsTestSession(t, ctx, script)
	closeStarted := false
	defer func() {
		if !closeStarted {
			_ = session.Close()
		}
	}()
	type readResult struct {
		output []byte
		err    error
	}
	readStarted := make(chan struct{})
	readDone := make(chan readResult, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		var output []byte
		buffer := make([]byte, 4096)
		first := true
		for {
			count, err := session.Read(buffer)
			output = append(output, buffer[:count]...)
			if first {
				close(readStarted)
				first = false
			}
			if err != nil {
				readDone <- readResult{output: output, err: err}
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case <-readStarted:
	case <-time.After(45 * time.Second):
		t.Fatal("rate-limited reader did not start")
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 45*time.Second)
	err := session.Wait(waitCtx)
	waitCancel()
	if err != nil {
		t.Fatalf("natural exit with a rate-limited reader: %v", err)
	}
	var result readResult
	select {
	case result = <-readDone:
	case <-time.After(45 * time.Second):
		t.Fatal("rate-limited reader did not reach EOF")
	}
	if !errors.Is(result.err, io.EOF) {
		t.Fatalf("rate-limited read = %v; want EOF", result.err)
	}
	if decoded := decodeWindowsPayload(t, result.output); !bytes.Equal(decoded, payload) {
		t.Fatalf("drained payload differs: got %d bytes, want %d", len(decoded), len(payload))
	}
	assertWindowsSessionEOF(t, session)
	closeStarted = true
	closeWindowsSessionWithin(t, session)
}

func TestSessionWindowsDelayedReadAfterNaturalExit(t *testing.T) {
	payload, script := windowsPayloadFixture(t, 1024+137)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := startWindowsTestSession(t, ctx, script)
	closeStarted := false
	defer func() {
		if !closeStarted {
			_ = session.Close()
		}
	}()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := session.Wait(waitCtx)
	waitCancel()
	if err != nil {
		t.Fatalf("natural exit before any read: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}
	if decoded := decodeWindowsPayload(t, output); !bytes.Equal(decoded, payload) {
		t.Fatalf("pending payload differs: got %d bytes, want %d", len(decoded), len(payload))
	}
	assertWindowsSessionEOF(t, session)
	closeStarted = true
	closeWindowsSessionWithin(t, session)
}

func TestSessionWindowsNaturalExitCloseRace(t *testing.T) {
	for iteration := 0; iteration < 12; iteration++ {
		t.Run(fmt.Sprintf("iteration-%02d", iteration), func(t *testing.T) {
			markerPath := filepath.Join(t.TempDir(), "ready")
			script := fmt.Sprintf("[IO.File]::WriteAllText('%s', 'ready'); Start-Sleep -Milliseconds 40; exit 0", strings.ReplaceAll(markerPath, "'", "''"))
			session := startWindowsTestSession(t, context.Background(), script)
			closeStarted := false
			defer func() {
				if !closeStarted {
					_ = session.Close()
				}
			}()
			waitForWindowsTestFile(t, markerPath, 10*time.Second)
			time.Sleep(time.Duration(iteration%8) * 5 * time.Millisecond)
			closeStarted = true
			closeWindowsSessionWithin(t, session)
		})
	}
}

func TestSessionWindowsCloseAfterNaturalExit(t *testing.T) {
	for iteration := 0; iteration < 12; iteration++ {
		t.Run(fmt.Sprintf("iteration-%02d", iteration), func(t *testing.T) {
			session := startWindowsTestSession(t, context.Background(), "[Console]::Out.Write('done'); exit 0")
			closeStarted := false
			defer func() {
				if !closeStarted {
					_ = session.Close()
				}
			}()
			waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := session.Wait(waitCtx)
			waitCancel()
			if err != nil {
				t.Fatalf("natural exit: %v", err)
			}
			closeStarted = true
			closeWindowsSessionWithin(t, session)
		})
	}
}

// GetProcessHandleCount covers only this test process; console hosts are filtered by leader descendants.
func TestSessionWindowsResourcesReturnToBaseline(t *testing.T) {
	exercise := func(natural bool) {
		command := "while ($true) { [Console]::Out.Write('x' * 65536); Start-Sleep -Milliseconds 50 }"
		if natural {
			command = "[Console]::Out.Write('cycle'); exit 0"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		session := startWindowsTestSession(t, ctx, command)
		closeStarted := false
		defer func() {
			if !closeStarted {
				_ = session.Close()
			}
		}()
		if natural {
			if _, err := io.ReadAll(session); err != nil {
				t.Fatal(err)
			}
			if err := session.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		closeStarted = true
		closeWindowsSessionWithin(t, session)
	}
	leaderPID := uint32(os.Getpid())
	func() {
		session := startWindowsTestSession(t, context.Background(), "while ($true) { [Console]::Out.Write('x' * 65536); Start-Sleep -Milliseconds 50 }")
		closeStarted := false
		defer func() {
			if !closeStarted {
				_ = session.Close()
			}
		}()
		openConsole, conhost := waitForWindowsOwnedConsoleHost(t, leaderPID, 15*time.Second)
		t.Logf("active PTY owned console hosts = OpenConsole:%d conhost:%d", openConsole, conhost)
		closeStarted = true
		closeWindowsSessionWithin(t, session)
	}()
	exercise(true)
	baselineSamples := sampleWindowsResources(t, leaderPID)
	baseline := windowsResourceSample{}
	for _, sample := range baselineSamples {
		baseline.handles = max(baseline.handles, sample.handles)
		baseline.openConsole = max(baseline.openConsole, sample.openConsole)
		baseline.conhost = max(baseline.conhost, sample.conhost)
	}
	for iteration := 0; iteration < 100; iteration++ {
		exercise(iteration%2 == 0)
	}
	samples := sampleWindowsResources(t, leaderPID)
	for index, sample := range samples {
		if sample.handles > baseline.handles+4 {
			t.Fatalf("handle sample %d = %d, baseline = %d; want <= %d (all samples %v)", index, sample.handles, baseline.handles, baseline.handles+4, samples)
		}
		if sample.openConsole > baseline.openConsole || sample.conhost > baseline.conhost {
			t.Fatalf("owned console hosts sample %d = OpenConsole:%d conhost:%d, baseline = OpenConsole:%d conhost:%d", index, sample.openConsole, sample.conhost, baseline.openConsole, baseline.conhost)
		}
	}
	if samples[0].handles < samples[1].handles && samples[1].handles < samples[2].handles {
		t.Fatalf("handle samples grew monotonically after settling: %v", samples)
	}
}

func windowsPayloadFixture(t *testing.T, size int) ([]byte, string) {
	t.Helper()
	payload := make([]byte, size)
	for index := range payload {
		payload[index] = byte(index % 251)
	}
	payloadPath := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("$payload = [IO.File]::ReadAllBytes('%s'); $encoded = [Convert]::ToBase64String($payload); [Console]::Out.WriteLine('NEXTERM-PAYLOAD-BEGIN'); for ($offset = 0; $offset -lt $encoded.Length; $offset += 64) { $length = [Math]::Min(64, $encoded.Length - $offset); [Console]::Out.WriteLine($encoded.Substring($offset, $length)) }; [Console]::Out.WriteLine('NEXTERM-PAYLOAD-END'); exit 0", strings.ReplaceAll(payloadPath, "'", "''"))
	return payload, script
}

func decodeWindowsPayload(t *testing.T, output []byte) []byte {
	t.Helper()
	begin := []byte("NEXTERM-PAYLOAD-BEGIN")
	end := []byte("NEXTERM-PAYLOAD-END")
	beginAt := bytes.Index(output, begin)
	if beginAt < 0 {
		t.Fatalf("payload begin marker missing from %d bytes: %q", len(output), output[:min(len(output), 512)])
	}
	remainder := output[beginAt+len(begin):]
	endAt := bytes.Index(remainder, end)
	if endAt < 0 {
		t.Fatalf("payload end marker missing from %d bytes: %q", len(output), output[:min(len(output), 512)])
	}
	encoded := strings.TrimSpace(string(remainder[:endAt]))
	encoded = strings.NewReplacer("\r", "", "\n", "").Replace(encoded)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return decoded
}

func assertWindowsSessionEOF(t *testing.T, session *Session) {
	t.Helper()
	var trailing [1]byte
	if count, err := session.Read(trailing[:]); count != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read after payload = %d, %v; want 0, EOF", count, err)
	}
}

func startWindowsTestSession(t *testing.T, ctx context.Context, command string) *Session {
	t.Helper()
	session, err := Start(ctx, Config{
		Path: "powershell.exe",
		Args: []string{"-NoLogo", "-NoProfile", "-Command", command},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func closeWindowsSessionWithin(t *testing.T, session *Session) {
	t.Helper()
	closed := make(chan error, 1)
	go func() { closed <- session.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Close exceeded the 5 second ConPTY close bound")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitForWindowsTestFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_, err := os.Stat(path)
		if err == nil {
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s was not created within %s", path, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWindowsConsoleHostOwnershipRequiresKnownDescendants(t *testing.T) {
	const leaderPID = uint32(100)
	processes := map[uint32]windowsProcessRecord{
		1:   {name: "services.exe"},
		100: {parentPID: 1, name: "pty.test.exe"},
		200: {parentPID: 100, name: "powershell.exe"},
		201: {parentPID: 200, name: "OpenConsole.exe"},
		202: {parentPID: 201, name: "conhost.exe"},
		300: {parentPID: 1, name: "other-package.test.exe"},
		301: {parentPID: 300, name: "OpenConsole.exe"},
		302: {parentPID: 300, name: "conhost.exe"},
	}
	openConsole, conhost, err := windowsOwnedConsoleHostCounts(leaderPID, processes)
	if err != nil {
		t.Fatal(err)
	}
	if openConsole != 1 || conhost != 1 {
		t.Fatalf("owned OpenConsole/conhost = %d/%d; want 1/1 with sibling package processes excluded", openConsole, conhost)
	}
	processes[400] = windowsProcessRecord{parentPID: 999, name: "conhost.exe"}
	if _, _, err := windowsOwnedConsoleHostCounts(leaderPID, processes); err == nil {
		t.Fatal("console host with an unknown parent chain was accepted")
	}
}

type windowsResourceSample struct {
	handles     int
	openConsole int
	conhost     int
}

func sampleWindowsResources(t *testing.T, leaderPID uint32) [3]windowsResourceSample {
	t.Helper()
	runtime.GC()
	time.Sleep(2 * time.Second)
	var samples [3]windowsResourceSample
	for index := range samples {
		samples[index].handles = windowsCurrentHandleCount(t)
		samples[index].openConsole, samples[index].conhost = windowsConsoleHostCounts(t, leaderPID)
		if index+1 < len(samples) {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return samples
}

var windowsGetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

func windowsCurrentHandleCount(t *testing.T) int {
	t.Helper()
	var count uint32
	result, _, callErr := windowsGetProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count)))
	if result == 0 {
		t.Fatalf("GetProcessHandleCount: %v", callErr)
	}
	return int(count)
}

type windowsProcessRecord struct {
	parentPID uint32
	name      string
}

func windowsOwnedConsoleHostCounts(leaderPID uint32, processes map[uint32]windowsProcessRecord) (int, int, error) {
	openConsole, conhost := 0, 0
	for pid, process := range processes {
		isOpenConsole := strings.EqualFold(process.name, "OpenConsole.exe")
		isConhost := strings.EqualFold(process.name, "conhost.exe")
		if !isOpenConsole && !isConhost {
			continue
		}
		owned, err := windowsProcessDescendsFrom(pid, leaderPID, processes)
		if err != nil {
			return 0, 0, fmt.Errorf("determine %s ownership for pid %d: %w", process.name, pid, err)
		}
		if !owned {
			continue
		}
		if isOpenConsole {
			openConsole++
		} else {
			conhost++
		}
	}
	return openConsole, conhost, nil
}

func windowsProcessDescendsFrom(pid, leaderPID uint32, processes map[uint32]windowsProcessRecord) (bool, error) {
	seen := make(map[uint32]struct{})
	current := pid
	for {
		if current == leaderPID {
			return true, nil
		}
		if _, exists := seen[current]; exists {
			return false, fmt.Errorf("parent cycle at pid %d", current)
		}
		seen[current] = struct{}{}
		process, exists := processes[current]
		if !exists {
			return false, fmt.Errorf("pid %d is absent from the process snapshot", current)
		}
		if process.parentPID == 0 {
			return false, nil
		}
		parent := process.parentPID
		if parent != leaderPID {
			if _, exists := processes[parent]; !exists {
				return false, fmt.Errorf("parent pid %d of pid %d is absent from the process snapshot", parent, current)
			}
		}
		current = parent
	}
}

func windowsConsoleHostCounts(t *testing.T, leaderPID uint32) (int, int) {
	t.Helper()
	processes := make(map[uint32]windowsProcessRecord)
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(snapshot, &entry)
	for {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		processes[entry.ProcessID] = windowsProcessRecord{
			parentPID: entry.ParentProcessID,
			name:      windows.UTF16ToString(entry.ExeFile[:]),
		}
		err = windows.Process32Next(snapshot, &entry)
	}
	openConsole, conhost, err := windowsOwnedConsoleHostCounts(leaderPID, processes)
	if err != nil {
		t.Fatal(err)
	}
	return openConsole, conhost
}

func waitForWindowsOwnedConsoleHost(t *testing.T, leaderPID uint32, timeout time.Duration) (int, int) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		openConsole, conhost := windowsConsoleHostCounts(t, leaderPID)
		if openConsole+conhost > 0 {
			return openConsole, conhost
		}
		if time.Now().After(deadline) {
			t.Fatalf("active PTY has no OpenConsole/conhost descendant of test leader %d after %s", leaderPID, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
