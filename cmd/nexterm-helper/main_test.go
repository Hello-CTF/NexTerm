package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == supervisor.HelperCommand || os.Args[1] == "--metadata") {
		os.Exit(run(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestHelperProbeProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], supervisor.HelperCommand, "--probe")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper probe exit: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != strconv.Itoa(supervisor.ProtocolVersion) {
		t.Fatalf("helper probe stdout = %q, want protocol version %d", got, supervisor.ProtocolVersion)
	}
}

func TestHelperMetadataProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "--metadata")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Version  string `json:"version"`
		Commit   string `json:"commit"`
		Protocol int    `json:"protocol"`
		SHA256   string `json:"sha256"`
	}
	if err := json.Unmarshal(output, &metadata); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["sha256"]; !ok {
		t.Fatalf("helper metadata lacks lowercase sha256: %s", output)
	}
	if _, ok := fields["SHA256"]; ok {
		t.Fatalf("helper metadata contains uppercase SHA256: %s", output)
	}
	if metadata.Version == "" || metadata.Commit == "" {
		t.Fatalf("helper metadata lacks version identity: %+v", metadata)
	}
	if metadata.Protocol != supervisor.ProtocolVersion {
		t.Fatalf("helper protocol = %d, want %d", metadata.Protocol, supervisor.ProtocolVersion)
	}
	if _, ok := fields["features"]; ok {
		t.Fatalf("helper metadata still lists negotiated features: %s", output)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	if want := hex.EncodeToString(digest[:]); metadata.SHA256 != want {
		t.Fatalf("helper SHA256 = %q, want executing file digest %q", metadata.SHA256, want)
	}
}
