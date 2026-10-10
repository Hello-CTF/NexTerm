package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var probeClient = &http.Client{Timeout: time.Second}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port, nil
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type instance struct {
	h       *harness
	binary  string
	dataDir string
	keyFile string
	logPath string
	auth    string
	webRoot string
	port    int
	cmd     *exec.Cmd
	waitCh  chan error
	exited  bool
	exitErr error
	logFile *os.File
}

func newInstance(h *harness, work, name, masterKey, auth, webRoot string) (*instance, error) {
	dataDir := filepath.Join(work, name)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	keyFile := filepath.Join(work, name+".master.key")
	if err := os.WriteFile(keyFile, []byte(masterKey+"\n"), 0o600); err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	return &instance{
		h: h, binary: h.bin, dataDir: dataDir, keyFile: keyFile,
		logPath: filepath.Join(work, name+".log"), auth: auth, webRoot: webRoot, port: port,
	}, nil
}

func (in *instance) start() error {
	in.stop()
	retries := 5
	for {
		args := []string{"--listen", fmt.Sprintf("127.0.0.1:%d", in.port), "--data-dir", in.dataDir, "--auth=" + in.auth}
		if in.webRoot != "" {
			args = append(args, "--web-root", in.webRoot)
		}
		var offset int64
		if info, err := os.Stat(in.logPath); err == nil {
			offset = info.Size()
		}
		logFile, err := os.OpenFile(in.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		in.logFile = logFile
		cmd := exec.Command(in.binary, args...)
		cmd.Dir = in.h.root
		cmd.Env = append(os.Environ(),
			"NEXTERM_DATA_DIR="+in.dataDir,
			fmt.Sprintf("NEXTERM_LISTEN=127.0.0.1:%d", in.port),
			"NEXTERM_MASTER_KEY_FILE="+in.keyFile,
			"NEXTERM_AUTH="+in.auth,
		)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err := cmd.Start(); err != nil {
			logFile.Close()
			in.logFile = nil
			return err
		}
		in.cmd = cmd
		in.exited = false
		in.exitErr = nil
		in.waitCh = make(chan error, 1)
		go func() { in.waitCh <- cmd.Wait() }()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case err := <-in.waitCh:
				in.exited = true
				in.exitErr = err
			default:
			}
			if in.exited {
				break
			}
			if healthy(in.port) {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !in.exited {
			return fmt.Errorf("server readiness timed out; log=%s", in.logPath)
		}
		code := exitCodeOf(in.exitErr)
		in.stop()
		output := readLogFrom(in.logPath, offset)
		if retries > 0 && (strings.Contains(output, "address already in use") || strings.Contains(output, "eaddrinuse")) {
			retries--
			port, err := freePort()
			if err != nil {
				return err
			}
			in.port = port
			continue
		}
		return fmt.Errorf("server exited with %d; log=%s", code, in.logPath)
	}
}

func healthy(port int) bool {
	resp, err := probeClient.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func readLogFrom(path string, offset int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	return strings.ToLower(string(raw))
}

func (in *instance) initCode() (string, error) {
	const marker = "一次性初始化码: "
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(in.logPath)
		for _, line := range strings.Split(string(raw), "\n") {
			if index := strings.Index(line, marker); index >= 0 {
				return strings.TrimSpace(line[index+len(marker):]), nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", fmt.Errorf("init code not found; log=%s", in.logPath)
}

func (in *instance) stop() {
	if in.cmd != nil && !in.exited {
		terminateProcess(in.cmd.Process)
		select {
		case err := <-in.waitCh:
			in.exited = true
			in.exitErr = err
		case <-time.After(8 * time.Second):
			in.cmd.Process.Kill()
			select {
			case err := <-in.waitCh:
				in.exited = true
				in.exitErr = err
			case <-time.After(3 * time.Second):
			}
		}
	}
	in.cmd = nil
	if in.logFile != nil {
		in.logFile.Close()
		in.logFile = nil
	}
}
