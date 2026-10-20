package services

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

func TestParseSystemdUnitsDistributionVariants(t *testing.T) {
	t.Parallel()
	output := "  UNIT                       LOAD      ACTIVE   SUB     DESCRIPTION\n" +
		"  nginx.service              loaded    active   running A high performance web server\n" +
		"● postgresql@14-main.service loaded    failed   failed  PostgreSQL Cluster 14-main\n" +
		"  cron.service               loaded    inactive dead    Regular background program processing daemon\n" +
		"  not-found.service          not-found inactive dead    not-found.service\n" +
		"  example.mount              loaded    active   mounted ignored mount unit\n" +
		"LOAD   = Reflects whether the unit definition was properly loaded.\n"
	units := ParseSystemdUnits(output)
	if len(units) != 4 {
		t.Fatalf("parsed %d units: %+v", len(units), units)
	}
	if units[0].Name != "nginx.service" || units[0].Active != "active" || units[0].Sub != "running" || units[0].Description != "A high performance web server" {
		t.Fatalf("nginx unit = %+v", units[0])
	}
	if units[1].Name != "postgresql@14-main.service" || units[1].Active != "failed" || units[1].Sub != "failed" {
		t.Fatalf("failed unit = %+v", units[1])
	}
	if units[2].Active != "inactive" || units[2].Sub != "dead" {
		t.Fatalf("inactive unit = %+v", units[2])
	}
	if units[3].Load != "not-found" {
		t.Fatalf("not-found unit = %+v", units[3])
	}
}

func TestSystemdHostMarksUnsupportedAndSudoFailuresSeparately(t *testing.T) {
	t.Parallel()
	unsupported := systemdHostResult(execbatch.Result{
		Status: execbatch.StatusExitError,
		Stderr: "System has not been booted with systemd as init system (PID 1).",
	}, true)
	if unsupported.Systemd != "unsupported" || unsupported.FailureKind != "systemd" {
		t.Fatalf("unsupported host = %+v", unsupported)
	}
	sudo := systemdHostResult(execbatch.Result{
		Status: execbatch.StatusExitError,
		Stderr: "sudo: a password is required",
	}, true)
	if sudo.Systemd != "unknown" || sudo.FailureKind != "sudo" {
		t.Fatalf("sudo failure = %+v", sudo)
	}
	if len(sudo.Units) != 0 {
		t.Fatalf("failed host returned units: %+v", sudo.Units)
	}
}
