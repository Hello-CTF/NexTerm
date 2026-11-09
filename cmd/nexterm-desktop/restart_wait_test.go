package main

import (
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

const restartWaitHelperEnv = "NEXTERM_TEST_RESTART_WAIT_HELPER"

func TestMain(m *testing.M) {
	if duration := os.Getenv(restartWaitHelperEnv); duration != "" {
		sleep, err := time.ParseDuration(duration)
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(sleep)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRestartArgsRoundTrip(t *testing.T) {
	original := []string{"--data-dir", "/x"}
	relaunch := restartArgs(original, 4242)
	if !reflect.DeepEqual(original, []string{"--data-dir", "/x"}) {
		t.Fatalf("restartArgs modified its input: %v", original)
	}
	cleaned, pid, err := stripRestartWaitPID(relaunch)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 4242 || !reflect.DeepEqual(cleaned, original) {
		t.Fatalf("stripRestartWaitPID(%v) = %v, %d", relaunch, cleaned, pid)
	}
}

func TestStripRestartWaitPID(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantArgs []string
		wantPID  int
		wantErr  bool
	}{
		{name: "absent", args: []string{"--auth", "off"}, wantArgs: []string{"--auth", "off"}},
		{name: "equals form", args: []string{"--restart-wait-pid=12", "nexterm://connect/a"}, wantArgs: []string{"nexterm://connect/a"}, wantPID: 12},
		{name: "separate value", args: []string{"--restart-wait-pid", "34", "--auth", "off"}, wantArgs: []string{"--auth", "off"}, wantPID: 34},
		{name: "repeated keeps last", args: []string{"--restart-wait-pid=1", "--restart-wait-pid=2"}, wantArgs: []string{}, wantPID: 2},
		{name: "missing value", args: []string{"--restart-wait-pid"}, wantErr: true},
		{name: "not a number", args: []string{"--restart-wait-pid=abc"}, wantErr: true},
		{name: "not positive", args: []string{"--restart-wait-pid=0"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, pid, err := stripRestartWaitPID(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got args %v pid %d", args, pid)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if pid != tc.wantPID || !reflect.DeepEqual(args, tc.wantArgs) {
				t.Fatalf("got %v, %d; want %v, %d", args, pid, tc.wantArgs, tc.wantPID)
			}
		})
	}
}

func startRestartWaitHelper(t *testing.T, sleep time.Duration) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^$")
	command.Env = append(os.Environ(), restartWaitHelperEnv+"="+sleep.String())
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap the child so its PID stops naming a process once it exits.
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-done
	})
	return command
}

func TestWaitForProcessExitReturnsWhenProcessExits(t *testing.T) {
	command := startRestartWaitHelper(t, 200*time.Millisecond)
	if !waitForProcessExit(command.Process.Pid, 30*time.Second) {
		t.Fatal("waitForProcessExit reported a timeout for a process that exits")
	}
}

func TestWaitForProcessExitTimesOutForRunningProcess(t *testing.T) {
	command := startRestartWaitHelper(t, time.Minute)
	if waitForProcessExit(command.Process.Pid, 100*time.Millisecond) {
		t.Fatal("waitForProcessExit reported exit for a running process")
	}
}
