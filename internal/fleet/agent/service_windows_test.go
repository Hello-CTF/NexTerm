//go:build windows

package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

const windowsQueryRunning = "SERVICE_NAME: NexTermAgent\n" +
	"        TYPE               : 10  WIN32_OWN_PROCESS\n" +
	"        STATE              : 4  RUNNING\n" +
	"                                (STOPPABLE, NOT_PAUSABLE, IGNORES_SHUTDOWN)\n" +
	"        WIN32_EXIT_CODE    : 0  (0x0)\n" +
	"        SERVICE_EXIT_CODE  : 0  (0x0)\n"

const windowsQueryStopped = "SERVICE_NAME: NexTermAgent\n" +
	"        TYPE               : 10  WIN32_OWN_PROCESS\n" +
	"        STATE              : 1  STOPPED\n" +
	"        WIN32_EXIT_CODE    : 1077  (0x435)\n"

const windowsQcAuto = "[SC] QueryServiceConfig SUCCESS\n" +
	"\n" +
	"SERVICE_NAME: NexTermAgent\n" +
	"        TYPE               : 10  WIN32_OWN_PROCESS\n" +
	"        START_TYPE         : 2   AUTO_START\n" +
	"        ERROR_CONTROL      : 1   NORMAL\n"

const windowsQcDisabled = "[SC] QueryServiceConfig SUCCESS\n" +
	"\n" +
	"SERVICE_NAME: NexTermAgent\n" +
	"        TYPE               : 10  WIN32_OWN_PROCESS\n" +
	"        START_TYPE         : 4   DISABLED\n" +
	"        ERROR_CONTROL      : 1   NORMAL\n"

func TestWindowsManagerInstallCommands(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string]string{
			"sc.exe stop " + windowsServiceName:   "[SC] ControlService FAILED 1062:\n",
			"sc.exe delete " + windowsServiceName: "[SC] OpenService FAILED 1060:\n",
			"sc.exe start " + windowsServiceName:  "",
		},
		errors: map[string]error{
			"sc.exe stop " + windowsServiceName:   realExitError(t),
			"sc.exe delete " + windowsServiceName: realExitError(t),
		},
	}
	manager := newWindowsManager(runner)
	exe := `C:\Program Files\NexTerm\nexterm-agent.exe`
	dataDir := `C:\Users\dev\AppData\Local\NexTerm`
	binPath := `"` + exe + `" run --data-dir "` + dataDir + `"`
	runner.responses["sc.exe create "+windowsServiceName+" binPath= "+binPath+" start= auto"] = ""
	if err := manager.Install(context.Background(), exe, dataDir); err != nil {
		t.Fatal(err)
	}
	expected := []recordedCommand{
		{name: "sc.exe", args: []string{"stop", windowsServiceName}},
		{name: "sc.exe", args: []string{"delete", windowsServiceName}},
		{name: "sc.exe", args: []string{"create", windowsServiceName, "binPath=", binPath, "start=", "auto"}},
		{name: "sc.exe", args: []string{"start", windowsServiceName}},
	}
	if !reflect.DeepEqual(runner.commands, expected) {
		t.Fatalf("install commands = %+v", runner.commands)
	}
}

func TestWindowsManagerInstallReportsAccessDenied(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string]string{
			"sc.exe stop " + windowsServiceName:   "",
			"sc.exe delete " + windowsServiceName: "",
		},
	}
	manager := newWindowsManager(runner)
	exe := `C:\NexTerm\nexterm-agent.exe`
	dataDir := `C:\NexTerm`
	binPath := `"` + exe + `" run --data-dir "` + dataDir + `"`
	createKey := "sc.exe create " + windowsServiceName + " binPath= " + binPath + " start= auto"
	runner.responses[createKey] = "[SC] OpenSCManager FAILED 5:\n\nAccess is denied.\n"
	runner.errors = map[string]error{createKey: realExitError(t)}
	err := manager.Install(context.Background(), exe, dataDir)
	if err == nil || !strings.Contains(err.Error(), "管理员") {
		t.Fatalf("access denied install must hint at elevation, got %v", err)
	}
}

func TestWindowsManagerStatusStates(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"sc.exe query " + windowsServiceName: windowsQueryRunning,
		"sc.exe qc " + windowsServiceName:    windowsQcAuto,
	}}
	manager := newWindowsManager(runner)
	ctx := context.Background()

	status := manager.Status(ctx)
	if !status.Installed || !status.Enabled || !status.Active || status.LastError != "" {
		t.Fatalf("running auto-start status = %+v", status)
	}

	runner.responses["sc.exe query "+windowsServiceName] = windowsQueryStopped
	runner.responses["sc.exe qc "+windowsServiceName] = windowsQcDisabled
	status = manager.Status(ctx)
	if !status.Installed || status.Enabled || status.Active || status.LastError != "" {
		t.Fatalf("stopped disabled status = %+v", status)
	}

	queryKey := "sc.exe query " + windowsServiceName
	runner.errors = map[string]error{queryKey: realExitError(t)}
	runner.responses[queryKey] = "[SC] EnumQueryServicesStatus:OpenService FAILED 1060:\n\nThe specified service does not exist as an installed service.\n"
	status = manager.Status(ctx)
	if status.Installed || status.Enabled || status.Active || status.LastError != "" {
		t.Fatalf("missing service must be a normal uninstalled result: %+v", status)
	}

	runner.responses[queryKey] = "[SC] OpenSCManager FAILED 5:\n\nAccess is denied.\n"
	status = manager.Status(ctx)
	if status.LastError == "" || !strings.Contains(status.LastError, "FAILED 5") {
		t.Fatalf("real query failure must surface in LastError: %+v", status)
	}

	runner.errors = map[string]error{}
	runner.responses[queryKey] = "SERVICE_NAME: NexTermAgent\n        TYPE : 10  WIN32_OWN_PROCESS\n"
	status = manager.Status(ctx)
	if status.LastError == "" || !strings.Contains(status.LastError, "STATE") {
		t.Fatalf("unparseable query output must fail honestly: %+v", status)
	}

	runner.responses[queryKey] = windowsQueryRunning
	runner.errors = map[string]error{"sc.exe qc " + windowsServiceName: realExitError(t)}
	runner.responses["sc.exe qc "+windowsServiceName] = "[SC] OpenService FAILED 5:\n"
	status = manager.Status(ctx)
	if !status.Installed || !status.Active || status.LastError == "" {
		t.Fatalf("qc failure must keep query facts and surface LastError: %+v", status)
	}
}

func TestWindowsManagerUninstallToleratesMissingAndStopped(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string]string{
			"sc.exe stop " + windowsServiceName:   "[SC] ControlService FAILED 1062:\n",
			"sc.exe delete " + windowsServiceName: "[SC] DeleteService SUCCESS\n",
		},
		errors: map[string]error{
			"sc.exe stop " + windowsServiceName: realExitError(t),
		},
	}
	manager := newWindowsManager(runner)
	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("stopped service uninstall must succeed: %v", err)
	}
	expected := []recordedCommand{
		{name: "sc.exe", args: []string{"stop", windowsServiceName}},
		{name: "sc.exe", args: []string{"delete", windowsServiceName}},
	}
	if !reflect.DeepEqual(runner.commands, expected) {
		t.Fatalf("uninstall commands = %+v", runner.commands)
	}

	runner.errors["sc.exe delete "+windowsServiceName] = realExitError(t)
	runner.responses["sc.exe delete "+windowsServiceName] = "[SC] OpenService FAILED 1060:\n"
	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("missing service uninstall must succeed: %v", err)
	}

	runner.responses["sc.exe stop "+windowsServiceName] = "[SC] ControlService FAILED 5:\n"
	if err := manager.Uninstall(context.Background()); err == nil {
		t.Fatal("access denied stop must fail")
	}
}

func TestWindowsStateAndStartTypeParsing(t *testing.T) {
	states := map[string]string{
		windowsQueryRunning: "RUNNING",
		windowsQueryStopped: "STOPPED",
		"        STATE              : 2  START_PENDING\n": "START_PENDING",
		"        STATE              : 3  STOP_PENDING\n":  "STOP_PENDING",
		"        STATE              : 7  PAUSED\n":        "PAUSED",
	}
	for output, expected := range states {
		state, failure := scState(output)
		if failure != "" || state != expected {
			t.Errorf("scState(%q) = %q, %q; want %q", output, state, failure, expected)
		}
	}
	if _, failure := scState("        STATE              : 99  WEIRD\n"); failure == "" {
		t.Error("unknown state word must fail")
	}

	startTypes := map[string]string{
		windowsQcAuto:     "AUTO_START",
		windowsQcDisabled: "DISABLED",
		"        START_TYPE         : 2   AUTO_START  (DELAYED)\n": "AUTO_START",
		"        START_TYPE         : 3   DEMAND_START\n":          "DEMAND_START",
	}
	for output, expected := range startTypes {
		startType, failure := scStartType(output)
		if failure != "" || startType != expected {
			t.Errorf("scStartType(%q) = %q, %q; want %q", output, startType, failure, expected)
		}
	}
	if _, failure := scStartType("        START_TYPE         : 2\n"); failure == "" {
		t.Error("missing start type word must fail")
	}
}
