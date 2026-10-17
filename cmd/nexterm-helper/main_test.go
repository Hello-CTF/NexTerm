package main

import (
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
		Version  string   `json:"version"`
		Commit   string   `json:"commit"`
		Protocol int      `json:"protocol"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(output, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Version == "" || metadata.Commit == "" {
		t.Fatalf("helper metadata lacks version identity: %+v", metadata)
	}
	if metadata.Protocol != supervisor.ProtocolVersion {
		t.Fatalf("helper protocol = %d, want %d", metadata.Protocol, supervisor.ProtocolVersion)
	}
	if len(metadata.Features) != 1 || metadata.Features[0] != "exec" {
		t.Fatalf("helper features = %v, want [exec]", metadata.Features)
	}
}
