package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	sessionCookie = "nexterm_session"
	csrfHeader    = "X-NexTerm-CSRF"
)

type checkEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type harness struct {
	root    string
	bin     string
	helper  string
	noBuild bool
	checks  []checkEntry
	failed  int
}

func (h *harness) check(name string, ok bool, detail string) {
	if ok {
		h.checks = append(h.checks, checkEntry{Name: name, Status: "passed"})
		fmt.Printf("  PASS %s\n", name)
		return
	}
	h.checks = append(h.checks, checkEntry{Name: name, Status: "failed", Detail: detail})
	h.failed++
	fmt.Printf("  FAIL %s: %s\n", name, detail)
}

func (h *harness) summarize() int {
	fmt.Printf("\n通过 %d 项, 失败 %d 项\n", len(h.checks)-h.failed, h.failed)
	if h.failed > 0 {
		return 1
	}
	return 0
}

func repoRoot() string {
	if wd, err := os.Getwd(); err == nil {
		dir := wd
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		return filepath.Dir(filepath.Dir(filepath.Dir(file)))
	}
	return "."
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func randomHex(size int) string {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		fatalf("%v", err)
	}
	return hex.EncodeToString(raw)
}

func (h *harness) build(binary, pkg string, server bool) error {
	version := os.Getenv("NEXTERM_RELEASE_VERSION")
	if version == "" {
		raw, err := os.ReadFile(filepath.Join(h.root, "wails.json"))
		if err != nil {
			return err
		}
		var wails struct {
			Info struct {
				Version string `json:"version"`
			} `json:"info"`
		}
		if err := json.Unmarshal(raw, &wails); err != nil {
			return err
		}
		version = wails.Info.Version
	}
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		return err
	}
	args := []string{"build", "-mod=readonly", "-trimpath", "-buildvcs=false"}
	if server {
		args = append(args, "-ldflags", "-s -w -X github.com/Hello-CTF/NexTerm/internal/version.Version="+version)
	}
	args = append(args, "-o", binary, pkg)
	cmd := exec.Command("go", args...)
	cmd.Dir = h.root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=go1.26.8")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build %s: %v", pkg, err)
	}
	return nil
}

func (h *harness) prepare() error {
	if h.noBuild {
		if _, err := os.Stat(h.helper); err != nil {
			fmt.Printf("--no-build: helper missing, building %s\n", h.helper)
			return h.build(h.helper, "./scripts/syncv2-helper", false)
		}
		return nil
	}
	if err := h.build(h.bin, "./cmd/nexterm-server", true); err != nil {
		return err
	}
	return h.build(h.helper, "./scripts/syncv2-helper", false)
}

func (h *harness) helperRunStdin(stdin string, args ...string) (string, error) {
	cmd := exec.Command(h.helper, args...)
	cmd.Dir = h.root
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("helper %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (h *harness) helperRun(args ...string) (string, error) {
	return h.helperRunStdin("", args...)
}

func initSuperadmin(h *harness, port int, code, username, password string) (int, map[string]interface{}, error) {
	out, err := h.helperRun("gen-dek", password)
	if err != nil {
		return 0, nil, err
	}
	bundle := map[string]interface{}{}
	if err := json.Unmarshal([]byte(out), &bundle); err != nil {
		return 0, nil, err
	}
	return newClient(port).request("POST", "/auth/init", map[string]interface{}{
		"code":              code,
		"username":          username,
		"password":          password,
		"dek_envelope":      bundle["dek_envelope"],
		"kdf_salt":          bundle["kdf_salt"],
		"kdf_params":        bundle["kdf_params"],
		"recovery_envelope": bundle["recovery_envelope"],
		"recovery_hash":     bundle["recovery_hash"],
	}, false)
}

func waitUntil(deadline time.Duration, what string, condition func() bool) error {
	until := time.Now().Add(deadline)
	for time.Now().Before(until) {
		if condition() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", what)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func tailBytes(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: e2e-harness <device|sharing|sync> [--bin PATH] [--helper PATH] [--no-build]")
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	scenario := os.Args[1]
	if scenario != "device" && scenario != "sharing" && scenario != "sync" {
		usage()
	}
	root := repoRoot()
	flags := flag.NewFlagSet("e2e-harness "+scenario, flag.ExitOnError)
	bin := flags.String("bin", filepath.Join(root, "target", "go-build", "nexterm-server-e2e"), "server 二进制路径")
	helper := flags.String("helper", filepath.Join(root, "target", "go-build", "syncv2-helper-e2e"), "syncv2-helper 二进制路径")
	noBuild := flags.Bool("no-build", false, "复用既有二进制, 不重新构建")
	flags.Parse(os.Args[2:])
	h := &harness{root: root, bin: *bin, helper: *helper, noBuild: *noBuild, checks: []checkEntry{}}
	if scenario == "sync" {
		os.Exit(runSync(h))
	}
	if err := h.prepare(); err != nil {
		fatalf("%v", err)
	}
	var err error
	if scenario == "device" {
		err = runDevice(h)
	} else {
		err = runSharing(h)
	}
	code := h.summarize()
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness error: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
