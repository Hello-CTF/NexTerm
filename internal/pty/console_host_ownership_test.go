package pty

import (
	"fmt"
	"strings"
	"testing"
)

func TestWindowsConsoleHostOwnershipRequiresKnownDescendants(t *testing.T) {
	const leaderPID = uint32(100)
	tests := []struct {
		name            string
		processes       map[uint32]windowsProcessRecord
		wantOpenConsole int
		wantConhost     int
	}{
		{
			name: "known descendants only",
			processes: map[uint32]windowsProcessRecord{
				1:   {name: "services.exe"},
				100: {parentPID: 1, name: "pty.test.exe"},
				200: {parentPID: 100, name: "powershell.exe"},
				201: {parentPID: 200, name: "OpenConsole.exe", creationTime: 1201},
				202: {parentPID: 201, name: "conhost.exe", creationTime: 1202},
				300: {parentPID: 1, name: "other-package.test.exe"},
				301: {parentPID: 300, name: "OpenConsole.exe"},
				302: {parentPID: 300, name: "conhost.exe"},
			},
			wantOpenConsole: 1,
			wantConhost:     1,
		},
		{
			name: "missing parent in unrelated real snapshot chain",
			processes: map[uint32]windowsProcessRecord{
				1:    {name: "services.exe"},
				100:  {parentPID: 1, name: "pty.test.exe"},
				200:  {parentPID: 100, name: "powershell.exe"},
				201:  {parentPID: 200, name: "OpenConsole.exe", creationTime: 1201},
				202:  {parentPID: 201, name: "conhost.exe", creationTime: 1202},
				860:  {parentPID: 748, name: "other-shell.exe"},
				6636: {parentPID: 860, name: "conhost.exe"},
			},
			wantOpenConsole: 1,
			wantConhost:     1,
		},
		{
			name: "unknown and cyclic chains",
			processes: map[uint32]windowsProcessRecord{
				100: {name: "pty.test.exe"},
				400: {parentPID: 999, name: "conhost.exe"},
				401: {parentPID: 402, name: "OpenConsole.exe"},
				402: {parentPID: 401, name: "other-shell.exe"},
			},
		},
		{
			name: "missing parent above leader",
			processes: map[uint32]windowsProcessRecord{
				100: {parentPID: 999, name: "pty.test.exe"},
				200: {parentPID: 100, name: "powershell.exe"},
				201: {parentPID: 200, name: "OpenConsole.exe", creationTime: 1201},
				202: {parentPID: 201, name: "conhost.exe", creationTime: 1202},
			},
			wantOpenConsole: 1,
			wantConhost:     1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ownership := newWindowsConsoleHostOwnership(leaderPID)
			openConsole, conhost, err := ownership.counts(test.processes)
			if err != nil {
				t.Fatal(err)
			}
			if openConsole != test.wantOpenConsole || conhost != test.wantConhost {
				t.Fatalf("owned OpenConsole/conhost = %d/%d; want %d/%d", openConsole, conhost, test.wantOpenConsole, test.wantConhost)
			}
		})
	}
}

func TestWindowsConsoleHostOwnershipDetectsLeakedDescendants(t *testing.T) {
	ownership := newWindowsConsoleHostOwnership(100)
	processes := map[uint32]windowsProcessRecord{
		100: {name: "pty.test.exe"},
		200: {parentPID: 100, name: "powershell.exe"},
	}
	baselineOpenConsole, baselineConhost, err := ownership.counts(processes)
	if err != nil {
		t.Fatal(err)
	}
	processes[201] = windowsProcessRecord{parentPID: 200, name: "OpenConsole.exe", creationTime: 1201}
	processes[202] = windowsProcessRecord{parentPID: 201, name: "conhost.exe", creationTime: 1202}
	openConsole, conhost, err := ownership.counts(processes)
	if err != nil {
		t.Fatal(err)
	}
	if openConsole <= baselineOpenConsole || conhost <= baselineConhost {
		t.Fatalf("leaked test descendants did not exceed baseline: baseline = %d/%d, leaked = %d/%d", baselineOpenConsole, baselineConhost, openConsole, conhost)
	}
}

func TestWindowsConsoleHostOwnershipIgnoresNewUnknownProcesses(t *testing.T) {
	ownership := newWindowsConsoleHostOwnership(100)
	processes := windowsConfirmedConsoleHostSnapshot()
	if _, _, err := ownership.counts(processes); err != nil {
		t.Fatal(err)
	}
	processes[860] = windowsProcessRecord{parentPID: 748, name: "other-shell.exe"}
	processes[6636] = windowsProcessRecord{parentPID: 860, name: "conhost.exe"}
	processes[400] = windowsProcessRecord{parentPID: 401, name: "OpenConsole.exe"}
	processes[401] = windowsProcessRecord{parentPID: 400, name: "other-shell.exe"}
	openConsole, conhost, err := ownership.counts(processes)
	if err != nil || openConsole != 1 || conhost != 1 {
		t.Fatalf("new unknown processes changed owned counts = %d/%d, %v", openConsole, conhost, err)
	}
}

func TestWindowsConsoleHostOwnershipRejectsConfirmedOwnedChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[uint32]windowsProcessRecord)
	}{
		{
			name: "intermediate parent removed after confirmation",
			mutate: func(processes map[uint32]windowsProcessRecord) {
				delete(processes, 200)
			},
		},
		{
			name: "owned processes disappeared after confirmation",
			mutate: func(processes map[uint32]windowsProcessRecord) {
				delete(processes, 201)
				delete(processes, 202)
			},
		},
		{
			name: "owned process identity changed",
			mutate: func(processes map[uint32]windowsProcessRecord) {
				process := processes[201]
				process.creationTime++
				processes[201] = process
			},
		},
		{
			name: "owned process chain became cyclic",
			mutate: func(processes map[uint32]windowsProcessRecord) {
				process := processes[200]
				process.parentPID = 201
				processes[200] = process
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ownership := newWindowsConsoleHostOwnership(100)
			processes := windowsConfirmedConsoleHostSnapshot()
			openConsole, conhost, err := ownership.counts(processes)
			if err != nil || openConsole != 1 || conhost != 1 {
				t.Fatalf("confirm owned console hosts = %d/%d, %v", openConsole, conhost, err)
			}
			test.mutate(processes)
			if _, _, err := ownership.counts(processes); err == nil {
				t.Fatal("confirmed owned process change was accepted")
			}
		})
	}
}

func TestWindowsConsoleHostOwnershipRejectsRegisteredHostAfterParentExit(t *testing.T) {
	ownership := newWindowsConsoleHostOwnership(100)
	var processes map[uint32]windowsProcessRecord
	for iteration := 0; iteration < 100; iteration++ {
		shellPID := uint32(1000 + iteration*10)
		openConsolePID := shellPID + 1
		conhostPID := shellPID + 2
		processes = map[uint32]windowsProcessRecord{
			100:            {name: "pty.test.exe"},
			shellPID:       {parentPID: 100, name: "powershell.exe"},
			openConsolePID: {parentPID: shellPID, name: "OpenConsole.exe", creationTime: uint64(1000 + iteration*10 + 1)},
			conhostPID:     {parentPID: openConsolePID, name: "conhost.exe", creationTime: uint64(1000 + iteration*10 + 2)},
		}
		openConsole, conhost, err := ownership.counts(processes)
		if err != nil || openConsole != 1 || conhost != 1 {
			t.Fatalf("iteration %d registration = %d/%d, %v", iteration, openConsole, conhost, err)
		}
		ownership.expectExit()
		if iteration == 99 {
			delete(processes, shellPID)
		}
	}
	if _, _, err := ownership.counts(processes); err == nil {
		t.Fatal("registered console hosts survived their parent after 100 exercises and were accepted")
	}
}

func TestWindowsConsoleHostOwnershipAllowsPIDReuseWithDifferentCreationTime(t *testing.T) {
	ownership := newWindowsConsoleHostOwnership(100)
	processes := windowsConfirmedConsoleHostSnapshot()
	if _, _, err := ownership.counts(processes); err != nil {
		t.Fatal(err)
	}
	ownership.expectExit()
	delete(processes, 202)
	process := processes[201]
	process.creationTime = 9201
	processes[201] = process
	openConsole, conhost, err := ownership.counts(processes)
	if err != nil || openConsole != 1 || conhost != 0 {
		t.Fatalf("reused PID with a new creation time = %d/%d, %v", openConsole, conhost, err)
	}
	delete(processes, 200)
	if _, _, err := ownership.counts(processes); err == nil {
		t.Fatal("reused PID was not tracked under its new identity")
	}
}

func TestWindowsConsoleHostOwnershipAllowsOnlyExpectedExit(t *testing.T) {
	ownership := newWindowsConsoleHostOwnership(100)
	processes := windowsConfirmedConsoleHostSnapshot()
	if _, _, err := ownership.counts(processes); err != nil {
		t.Fatal(err)
	}
	ownership.expectExit()
	delete(processes, 200)
	delete(processes, 201)
	delete(processes, 202)
	openConsole, conhost, err := ownership.counts(processes)
	if err != nil || openConsole != 0 || conhost != 0 {
		t.Fatalf("expected exit = %d/%d, %v", openConsole, conhost, err)
	}
	processes[201] = windowsProcessRecord{parentPID: 999, name: "OpenConsole.exe", creationTime: 1201}
	if _, _, err := ownership.counts(processes); err == nil {
		t.Fatal("previously owned process reappeared after expected exit without being rejected")
	}
}

func windowsConfirmedConsoleHostSnapshot() map[uint32]windowsProcessRecord {
	return map[uint32]windowsProcessRecord{
		100: {name: "pty.test.exe"},
		200: {parentPID: 100, name: "powershell.exe"},
		201: {parentPID: 200, name: "OpenConsole.exe", creationTime: 1201},
		202: {parentPID: 201, name: "conhost.exe", creationTime: 1202},
	}
}

type windowsProcessRecord struct {
	parentPID    uint32
	name         string
	creationTime uint64
}

type windowsOwnedConsoleHost struct {
	name         string
	creationTime uint64
	expectExit   bool
}

type windowsConsoleHostOwnership struct {
	leaderPID uint32
	owned     map[uint32]windowsOwnedConsoleHost
}

func newWindowsConsoleHostOwnership(leaderPID uint32) *windowsConsoleHostOwnership {
	return &windowsConsoleHostOwnership{leaderPID: leaderPID, owned: make(map[uint32]windowsOwnedConsoleHost)}
}

func (o *windowsConsoleHostOwnership) expectExit() {
	for pid, owned := range o.owned {
		owned.expectExit = true
		o.owned[pid] = owned
	}
}

func (o *windowsConsoleHostOwnership) counts(processes map[uint32]windowsProcessRecord) (int, int, error) {
	for pid, owned := range o.owned {
		process, exists := processes[pid]
		if !exists {
			if !owned.expectExit {
				return 0, 0, fmt.Errorf("previously owned %s for pid %d is absent from the process snapshot", owned.name, pid)
			}
			continue
		}
		if process.creationTime == 0 {
			return 0, 0, fmt.Errorf("verify previously owned %s for pid %d: process creation time is unavailable", owned.name, pid)
		}
		if !strings.EqualFold(process.name, owned.name) || process.creationTime != owned.creationTime {
			if !owned.expectExit {
				return 0, 0, fmt.Errorf("previously owned %s for pid %d changed process identity", owned.name, pid)
			}
			delete(o.owned, pid)
			continue
		}
		if owned.expectExit {
			return 0, 0, fmt.Errorf("previously owned %s for pid %d remains after expected exit", owned.name, pid)
		}
		if !windowsProcessDescendsFrom(pid, o.leaderPID, processes) {
			return 0, 0, fmt.Errorf("previously owned %s for pid %d has an incomplete ownership chain", owned.name, pid)
		}
	}
	openConsole, conhost := 0, 0
	for pid, process := range processes {
		isOpenConsole := strings.EqualFold(process.name, "OpenConsole.exe")
		isConhost := strings.EqualFold(process.name, "conhost.exe")
		if !isOpenConsole && !isConhost {
			continue
		}
		if _, exists := o.owned[pid]; !exists {
			if !windowsProcessDescendsFrom(pid, o.leaderPID, processes) {
				continue
			}
			if process.creationTime == 0 {
				return 0, 0, fmt.Errorf("track owned %s for pid %d: process creation time is unavailable", process.name, pid)
			}
			o.owned[pid] = windowsOwnedConsoleHost{name: process.name, creationTime: process.creationTime}
		}
		if isOpenConsole {
			openConsole++
		} else {
			conhost++
		}
	}
	return openConsole, conhost, nil
}

func windowsProcessDescendsFrom(pid, leaderPID uint32, processes map[uint32]windowsProcessRecord) bool {
	seen := make(map[uint32]struct{})
	current := pid
	for {
		if current == leaderPID {
			return true
		}
		if _, exists := seen[current]; exists {
			return false
		}
		seen[current] = struct{}{}
		process, exists := processes[current]
		if !exists || process.parentPID == 0 {
			return false
		}
		current = process.parentPID
	}
}
