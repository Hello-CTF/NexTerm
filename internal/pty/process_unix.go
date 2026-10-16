//go:build unix

package pty

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var unixProcessSnapshot = func() ([]byte, error) {
	return exec.Command("/bin/ps", "-A", "-o", "pid=,pgid=,stat=").Output()
}

var unixGetSessionID = unix.Getsid
var unixGetProcessGroupID = unix.Getpgid
var unixReadProcessIdentity = readUnixProcessIdentity
var unixKill = syscall.Kill

func startProcess(cmd *exec.Cmd) (*Process, error) {
	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	process, err := newStartedProcess(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return process, nil
}

func prepareCommand(cmd *exec.Cmd) {
	attrs := &syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		copy := *cmd.SysProcAttr
		attrs = &copy
	}
	attrs.Setpgid = true
	attrs.Pgid = 0
	cmd.SysProcAttr = attrs
}

type unixProcessTree struct {
	process  *os.Process
	session  bool
	identity string
}

func newStartedProcess(cmd *exec.Cmd) (*Process, error) {
	tree := &unixProcessTree{
		process: cmd.Process,
		session: cmd.SysProcAttr != nil && cmd.SysProcAttr.Setsid,
	}
	if tree.session {
		identity, err := unixReadProcessIdentity(cmd.Process.Pid)
		if err != nil {
			return nil, err
		}
		tree.identity = identity
	}
	return newProcess(tree, cmd.Wait), nil
}

func (t *unixProcessTree) Kill() error {
	if t.session {
		if t.identity == "" {
			return errors.New("PTY session leader start identity is unavailable")
		}
		return killUnixSession(t.process.Pid, t.identity)
	}
	err := unixKill(-t.process.Pid, syscall.SIGKILL)
	if err == nil {
		return nil
	}
	err = t.process.Kill()
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func killUnixSession(sessionID int, expectedIdentity string) error {
	verifyLeader := func() error {
		sid, sidErr := unixGetSessionID(sessionID)
		group, groupErr := unixGetProcessGroupID(sessionID)
		identity, identityErr := unixReadProcessIdentity(sessionID)
		if sidErr != nil || groupErr != nil || identityErr != nil {
			return errors.Join(sidErr, groupErr, identityErr)
		}
		if sid != sessionID || group != sessionID {
			return fmt.Errorf("PTY session %d leader identity cannot be verified", sessionID)
		}
		if identity != expectedIdentity {
			return fmt.Errorf("PTY session %d leader start identity changed", sessionID)
		}
		return nil
	}
	for round := 0; ; round++ {
		groups, err := unixSessionProcessGroups(sessionID)
		if err != nil {
			return err
		}
		if len(groups) == 0 {
			return nil
		}
		if err := verifyLeader(); err != nil {
			return fmt.Errorf("verify PTY session %d leader after scan: %w", sessionID, err)
		}
		if round == 32 {
			return fmt.Errorf("PTY session %d cleanup did not converge", sessionID)
		}
		if len(groups) == 1 {
			if err := verifyLeader(); err != nil {
				return fmt.Errorf("verify PTY session %d leader: %w", sessionID, err)
			}
			if err := unixKill(-sessionID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("kill PTY session leader %d: %w", sessionID, err)
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		remaining := make([]int, 0, len(groups))
		for group := range groups {
			if group != sessionID {
				remaining = append(remaining, group)
			}
		}
		sort.Ints(remaining)
		for _, group := range remaining {
			if err := verifyLeader(); err != nil {
				return fmt.Errorf("verify PTY session %d leader: %w", sessionID, err)
			}
			members := groups[group]
			if len(members) == 0 {
				continue
			}
			pid := members[0]
			sid, sidErr := unixGetSessionID(pid)
			currentGroup, groupErr := unixGetProcessGroupID(pid)
			if errors.Is(sidErr, syscall.ESRCH) || errors.Is(groupErr, syscall.ESRCH) {
				continue
			}
			if sidErr != nil || groupErr != nil {
				return errors.Join(sidErr, groupErr)
			}
			if sid != sessionID || currentGroup != group {
				return fmt.Errorf("PTY session process %d identity changed before kill", pid)
			}
			if err := unixKill(-group, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("kill PTY session process group %d: %w", group, err)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func unixSessionProcessGroups(sessionID int) (map[int][]int, error) {
	output, err := unixProcessSnapshot()
	if err != nil {
		return nil, fmt.Errorf("list PTY session processes: %w", err)
	}
	groups := make(map[int][]int)
	for _, line := range bytes.Split(output, []byte("\n")) {
		fields := bytes.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 && len(fields) != 3 {
			return nil, fmt.Errorf("unexpected process list line %q", string(line))
		}
		pid, err := strconv.Atoi(string(fields[0]))
		if err != nil {
			return nil, fmt.Errorf("parse process ID %q: %w", string(fields[0]), err)
		}
		group, err := strconv.Atoi(string(fields[1]))
		if err != nil {
			return nil, fmt.Errorf("parse process group %q: %w", string(fields[1]), err)
		}
		if pid <= 0 || group <= 0 {
			continue
		}
		sid, err := unixGetSessionID(pid)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get session for process %d: %w", pid, err)
		}
		if sid != sessionID {
			continue
		}
		if len(fields) == 3 && len(fields[2]) > 0 && fields[2][0] == 'Z' {
			continue
		}
		groups[group] = append(groups[group], pid)
	}
	return groups, nil
}

func (t *unixProcessTree) Close() error {
	return nil
}

func processSignal(state os.ProcessState) string {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}
