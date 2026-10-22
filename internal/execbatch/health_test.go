package execbatch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func healthTestSection(name, body string, status int) string {
	return fmt.Sprintf("%s%s\n%s\n%s%s:%d\n", healthSectionMarker, name, body, healthEndMarker, name, status)
}

func healthTestOutput(uptime, load, disk, memory, docker string) string {
	return healthTestSection("uptime", uptime, 0) +
		healthTestSection("load", load, 0) +
		healthTestSection("disk", disk, 0) +
		healthTestSection("memory", memory, 0) +
		healthTestSection("docker", docker, 0)
}

func TestParseHealthOutput(t *testing.T) {
	output := healthTestOutput(
		" 10:20:01 up 2 days, 3:04, 1 user, load average: 9.00, 9.00, 9.00",
		"0.50 0.40 0.30 1/100 123",
		"Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 1000 420 580 42% /",
		"              total        used        free      shared  buff/cache   available\nMem:     1000        600         50           0         350         300",
		"TYPE TOTAL ACTIVE SIZE RECLAIMABLE\nImages 5 2 10GB 1.5GB (15%)\nContainers 2 1 1GB 0B (0%)\nLocal Volumes 3 1 2GiB 1GiB (50%)\nBuild Cache 4 0 500MB 500MB",
	)
	metrics := parseHealthOutput(output)
	if len(metrics.errors) != 0 {
		t.Fatalf("metric errors = %+v", metrics.errors)
	}
	if metrics.uptimeSeconds == nil || *metrics.uptimeSeconds != 2*86400+3*3600+4*60 {
		t.Fatalf("uptime = %v", metrics.uptimeSeconds)
	}
	if metrics.load1 == nil || *metrics.load1 != 0.5 || metrics.load5 == nil || *metrics.load5 != 0.4 || metrics.load15 == nil || *metrics.load15 != 0.3 {
		t.Fatalf("load = %v %v %v", metrics.load1, metrics.load5, metrics.load15)
	}
	if metrics.diskUsedPercent == nil || *metrics.diskUsedPercent != 42 {
		t.Fatalf("disk = %v", metrics.diskUsedPercent)
	}
	if metrics.memoryUsedPercent == nil || math.Abs(*metrics.memoryUsedPercent-70) > 0.0001 {
		t.Fatalf("memory = %v", metrics.memoryUsedPercent)
	}
	wantDocker := int64(1.5*1e9) + 1<<30 + 500*1e6
	if metrics.dockerReclaimableBytes == nil || *metrics.dockerReclaimableBytes != wantDocker {
		t.Fatalf("docker = %v, want %d", metrics.dockerReclaimableBytes, wantDocker)
	}
}

func TestParseUptimeVariants(t *testing.T) {
	tests := []struct {
		output string
		want   int64
	}{
		{"10:00:00 up 45 sec, 1 user, load average: 0, 0, 0", 45},
		{"10:00:00 up 3 min, 1 user, load average: 0, 0, 0", 180},
		{"10:00:00 up 1:15, 2 users, load average: 0, 0, 0", 4500},
		{"10:00 up 2 days, 3 hrs, 4 min, 2 users, load averages: 0 0 0", 2*86400 + 3*3600 + 4*60},
		{"10:00 up 1 week, 2 days, 3:04, 1 user, load average: 0, 0, 0", 9*86400 + 3*3600 + 4*60},
		{"10:00 up 2 hours 5 minutes, 1 user, load average: 0, 0, 0", 7500},
		{"10:00 up 01:02:03, 1 user, load average: 0, 0, 0", 3723},
	}
	for _, test := range tests {
		t.Run(test.output, func(t *testing.T) {
			got, err := parseUptimeSeconds(test.output)
			if err != nil || got != test.want {
				t.Fatalf("parseUptimeSeconds() = %d, %v; want %d", got, err, test.want)
			}
		})
	}
	for _, output := range []string{"uptime: not found", "10:00 up someday, 1 user", "10:00 up 1:99, 1 user"} {
		if _, err := parseUptimeSeconds(output); err == nil {
			t.Fatalf("parseUptimeSeconds(%q) unexpectedly succeeded", output)
		}
	}
}

func TestParseDiskVariants(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   float64
	}{
		{"standard", "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 1000 420 580 42% /", 42},
		{"wrapped device", "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/mapper/a-very-long-filesystem-name\n1000 700 300 70% /", 70},
		{"other mount ignored", "/dev/sda1 1000 420 580 42% /var\n/dev/sdb1 1000 100 900 10% /", 10},
		{"decimal", "/dev/sda1 1000 420 580 42.5% /", 42.5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseDiskUsedPercent(test.output)
			if err != nil || got != test.want {
				t.Fatalf("parseDiskUsedPercent() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestParseMemoryVariants(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   float64
	}{
		{"available", "total used free shared buff/cache available\nMem: 1000 600 50 0 350 300", 70},
		{"buffers and cache", "total used free shared buffers cached\nMem: 1000 600 50 0 100 200", 30},
		{"used only", "total used free\nMem: 1000 400 600", 40},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseMemoryUsedPercent(test.output)
			if err != nil || math.Abs(got-test.want) > 0.0001 {
				t.Fatalf("parseMemoryUsedPercent() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestParseHealthOutputKeepsPartialMetricsAndDockerFailureVisible(t *testing.T) {
	output := healthTestSection("uptime", "10:00 up 5 min, 1 user, load average: 0, 0, 0", 0) +
		healthTestSection("load", "0.1 0.2 0.3", 0) +
		healthTestSection("disk", "/dev/sda 100 50 50 50% /", 0) +
		healthTestSection("memory", "total used free\nMem: 100 25 75", 0) +
		healthTestSection("docker", "/bin/sh: docker: not found", 127)
	metrics := parseHealthOutput(output)
	if metrics.uptimeSeconds == nil || metrics.diskUsedPercent == nil || metrics.memoryUsedPercent == nil {
		t.Fatalf("valid metrics were dropped: %+v", metrics)
	}
	if len(metrics.errors) != 1 || metrics.errors[0].Metric != "docker" {
		t.Fatalf("docker errors = %+v", metrics.errors)
	}
	if !strings.Contains(metrics.errors[0].Error, "not found") {
		t.Fatalf("docker diagnostics = %+v", metrics.errors)
	}
}

func TestProbeHealthUsesOneScriptPerConnectedHostWithoutWrites(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	output := healthTestOutput("10:00 up 5 min, 1 user, load average: 0, 0, 0", "0.1 0.2 0.3", "/dev/sda 100 50 50 50% /", "total used free\nMem: 100 25 75", "Images 1 0 1GB 100MB")
	var execCalls atomic.Int32
	healthy := newFakeTransport(func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		if command != healthProbeCommand {
			return base.ExecResult{}, fmt.Errorf("unexpected health command")
		}
		code := 0
		return base.ExecResult{Stdout: output, ExitCode: &code}, nil
	})
	failed := newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		return base.ExecResult{}, errors.New("connection reset")
	})
	resolver := &fakeResolver{
		targets: map[string]Target{
			"healthy": targetFor("healthy", healthy),
			"failed":  targetFor("failed", failed),
		},
		errors: map[string]error{"resolve-failed": errors.New("session disappeared")},
	}
	service := New(Config{Resolver: resolver, History: database, Auditor: database})
	ownerCtx := execBatchHistoryContext("owner-a")
	historyBefore, err := database.ExecBatchHistoryList(ownerCtx, store.ExecBatchHistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	auditBefore, err := database.AuditCount(ownerCtx, store.AuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := service.ProbeHealth(t.Context(), "owner-a", ExecBatchHealthRequest{
		BatchID: "health-one", Targets: []ExecBatchHealthTarget{
			{AssetID: "asset-healthy", SessionID: "healthy"},
			{AssetID: "asset-failed", SessionID: "failed"},
			{AssetID: "asset-resolve-failed", SessionID: "resolve-failed"},
			{AssetID: "asset-disconnected"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 4 {
		t.Fatalf("results = %+v", batch.Results)
	}
	if got := execCalls.Load(); got != 2 {
		t.Fatalf("executed %d scripts, want one per connected target", got)
	}
	if !batch.Results[0].Reachable || batch.Results[0].UptimeSeconds == nil || batch.Results[0].DockerReclaimableBytes == nil {
		t.Fatalf("healthy result = %+v", batch.Results[0])
	}
	if batch.Results[1].Reachable || !strings.Contains(batch.Results[1].Error, "connection reset") {
		t.Fatalf("failed result = %+v", batch.Results[1])
	}
	if !strings.Contains(batch.Results[2].Error, "session disappeared") || !strings.Contains(batch.Results[3].Error, ErrDisconnected.Error()) {
		t.Fatalf("resolution results = %+v", batch.Results)
	}
	historyAfter, err := database.ExecBatchHistoryList(ownerCtx, store.ExecBatchHistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	auditAfter, err := database.AuditCount(ownerCtx, store.AuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if historyAfter.Total != historyBefore.Total {
		t.Fatalf("health probe changed batch history from %d to %d rows", historyBefore.Total, historyAfter.Total)
	}
	if auditAfter != auditBefore {
		t.Fatalf("health probe changed audit history from %d to %d rows", auditBefore, auditAfter)
	}
}

func TestProbeHealthCancelStopsInflightExec(t *testing.T) {
	started := make(chan struct{}, 1)
	transport := newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return base.ExecResult{}, ctx.Err()
	})
	resolver := &fakeResolver{targets: map[string]Target{"slow": targetFor("slow", transport)}}
	service := New(Config{Resolver: resolver})
	type outcome struct {
		batch ExecBatchHealth
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		batch, err := service.ProbeHealth(t.Context(), "owner-a", ExecBatchHealthRequest{
			BatchID: "health-cancel", Targets: []ExecBatchHealthTarget{{AssetID: "asset-slow", SessionID: "slow"}},
		})
		done <- outcome{batch: batch, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("health exec did not start")
	}
	if _, err := service.Cancel(t.Context(), "owner-a", "health-cancel"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if len(result.batch.Results) != 1 || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("canceled health result = %+v", result.batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("health probe did not finish after cancellation")
	}
}
