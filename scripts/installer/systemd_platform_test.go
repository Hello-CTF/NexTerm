//go:build !windows

package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSystemdNonLinuxFailsVisibly(t *testing.T) {
	for _, kernel := range []string{"Darwin", "Windows_NT", "MINGW64_NT-10.0"} {
		t.Run(kernel, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			got := s.mustFail(s.payload("v1"), installOptions{uname: kernel})
			expect(t, strings.Contains(got.stderr, "unsupported operating system: "+kernel), "stderr: %s", got.stderr)
			expect(t, !s.exists("opt/nexterm") && !s.exists("etc/nexterm"), "directories were created")
			expect(t, len(s.systemctlCalls()) == 0 && len(s.opensslCalls()) == 0 && !s.baseExists("useradd.log"), "tools were invoked")
		})
	}
}

func TestSystemdNonSystemdInitFailsVisibly(t *testing.T) {
	for _, variant := range []string{"sysv", "none"} {
		t.Run(variant, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			got := s.mustFail(s.payload("v1"), installOptions{systemd: variant})
			expect(t, strings.Contains(got.stderr, "systemd is required") && strings.Contains(got.stderr, "non-systemd"), "stderr: %s", got.stderr)
			expect(t, !s.exists("opt/nexterm") && !s.exists("etc/nexterm") && len(s.systemctlCalls()) == 0, "install mutated the root")
		})
	}
}

func TestSystemdInitSymlinkEvidenceAccepted(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	got := s.mustInstall(s.payload("v1"), installOptions{systemd: "systemd-link"})
	expect(t, strings.Contains(got.stdout, "init: systemd") && strings.Contains(got.stdout, "platform: Linux"), "stdout: %s", got.stdout)
}

func TestSystemdDistroDetection(t *testing.T) {
	for _, release := range []string{
		osReleaseUbuntu,
		"ID=debian\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n",
		"ID=rocky\nID_LIKE=\"rhel centos fedora\"\nPRETTY_NAME=\"Rocky Linux 9.4\"\n",
		"ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nPRETTY_NAME=\"Linux Mint 22\"\n",
		"ID=arch\nPRETTY_NAME=\"Arch Linux\"\n",
		"ID=opensuse-tumbleweed\nID_LIKE=\"opensuse suse\"\nPRETTY_NAME=\"openSUSE Tumbleweed\"\n",
	} {
		t.Run(strings.SplitN(release, "\n", 2)[0], func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			got := s.mustInstall(s.payload("v1"), installOptions{osRelease: release})
			expect(t, strings.Contains(got.stdout, "(supported)"), "stdout: %s", got.stdout)
			expect(t, !strings.Contains(got.stdout, "not in the supported") && !strings.Contains(got.stdout, "cannot identify the distribution"), "stdout: %s", got.stdout)
		})
	}
	t.Run("unknown", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		got := s.mustInstall(s.payload("v1"), installOptions{osRelease: "ID=weirdos\nPRETTY_NAME=\"WeirdOS 1.0\"\n"})
		expect(t, strings.Contains(got.stdout, "not in the supported") && strings.Contains(got.stdout, "WeirdOS 1.0") && strings.Contains(got.stdout, "WARNING"), "stdout: %s", got.stdout)
	})
	t.Run("missing", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		got := s.mustInstall(s.payload("v1"), installOptions{noOSRelease: true})
		expect(t, strings.Contains(got.stdout, "cannot identify the distribution") && strings.Contains(got.stdout, "WARNING"), "stdout: %s", got.stdout)
	})
}

func TestSystemdFirstInstallReportsActiveService(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	got := s.mustInstall(s.payload("v1"), installOptions{})
	expect(t, strings.Contains(got.stdout, "nexterm-server.service is active"), "stdout: %s", got.stdout)

	s = newSandbox(t, stubOptions{})
	got = s.mustInstall(s.payload("v1"), installOptions{failStart: true})
	expect(t, strings.Contains(got.stdout, "did not report active") && strings.Contains(got.stdout, "journalctl -u nexterm-server.service") && strings.Contains(got.stdout, "WARNING"), "stdout: %s", got.stdout)
}

func TestSystemdConsoleOutputRedactsKeyMaterial(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	got := s.mustInstall(s.payload("v1"), installOptions{})
	expect(t, s.read("etc/nexterm/master.key") == fakeKeyHex+"\n", "key not generated")
	expect(t, !strings.Contains(got.stdout+got.stderr, fakeKeyHex), "generated key material must never reach the console")

	s.write("etc/nexterm/master.key", "preexisting-super-secret-key\n")
	s.removeLog("openssl.log")
	upgrade := s.mustInstall(s.payload("v2"), installOptions{active: "nexterm-server.service"})
	combined := upgrade.stdout + upgrade.stderr
	expect(t, !strings.Contains(combined, "preexisting-super-secret-key") && !strings.Contains(combined, fakeKeyHex), "key material reached the console")
	expect(t, len(s.opensslCalls()) == 0, "no key generation while the key exists")
}

func TestSystemdRollbackRequiresSupportedPlatform(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	v1 := s.seedFirstInstall()
	v2 := s.payload("v2")
	s.mustFail(v2, installOptions{failAt: "units", active: "nexterm-server.service"})
	v2Binary := readPath(t, filepath.Join(v2, "nexterm-server"))

	got := s.mustFail(v2, installOptions{args: []string{"--rollback"}, uname: "Darwin"})
	expect(t, strings.Contains(got.stderr, "unsupported operating system: Darwin"), "stderr: %s", got.stderr)
	expect(t, s.read("opt/nexterm/nexterm-server") == v2Binary, "payload must be untouched")

	got = s.mustFail(v2, installOptions{args: []string{"--rollback"}, systemd: "none"})
	expect(t, strings.Contains(got.stderr, "systemd is required"), "stderr: %s", got.stderr)
	expect(t, s.read("opt/nexterm/nexterm-server") == v2Binary, "payload must be untouched")

	s.mustInstall(v2, installOptions{args: []string{"--rollback"}, active: "nexterm-server.service"})
	expect(t, s.read("opt/nexterm/nexterm-server") == readPath(t, filepath.Join(v1, "nexterm-server")), "supported rollback must restore v1")
}

func TestSystemdMissingSystemctlFailsVisibly(t *testing.T) {
	s := newSandbox(t, stubOptions{noSystemctl: true})
	got := s.mustFail(s.payload("v1"), installOptions{})
	expect(t, strings.Contains(got.stderr, "systemctl not found"), "stderr: %s", got.stderr)
	expect(t, !s.exists("opt/nexterm"), "failed preflight must not create directories")
}

const passwordMarker = "初始密码: "

func TestSystemdBootstrapPrintsPasswordBeforeServiceStart(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	got := s.mustInstall(s.payload("v1"), installOptions{initAdmin: true, initAdminStdout: "nexterm-server: 初始密码: bootstrap-random-password-123（仅显示一次，请立即妥善保存）"})
	expect(t, strings.Contains(got.stdout, "bootstrap-random-password-123") && strings.Contains(got.stdout, "super-admin created"), "stdout: %s", got.stdout)
	expect(t, strings.Join(s.initAdminCalls(), ",") == "init-admin", "calls: %v", s.initAdminCalls())
	env := s.initAdminEnv()
	expectValue(t, env, "NEXTERM_AUTH", "on")
	expectValue(t, env, "NEXTERM_DATA_DIR", "/var/lib/nexterm")
	expectValue(t, env, "NEXTERM_DB", "sqlite")
	expectValue(t, env, "NEXTERM_ADMIN_PASSWORD", "")
	expectValue(t, env, "NEXTERM_ADMIN_USERNAME", "")
	expectPasswordBeforeStart(t, got.stdout)
	expect(t, contains(s.systemctlCalls(), "enable --now nexterm-server.service"), "calls: %v", s.systemctlCalls())
}

func expectPasswordBeforeStart(t *testing.T, stdout string) {
	t.Helper()
	printed, started := strings.Index(stdout, passwordMarker), strings.Index(stdout, "enabled and started nexterm-server.service")
	expect(t, printed >= 0 && started >= 0 && printed < started, "the password must reach the console before the service starts: %s", stdout)
}

func TestSystemdBootstrapSuppliedPasswordPassthrough(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	got := s.mustInstall(s.payload("v1"), installOptions{initAdmin: true, adminPassword: password("synthetic-password-1"), initAdminStdout: "nexterm-server: 初始密码: synthetic-password-1（仅显示一次，请立即妥善保存）"})
	expectValue(t, s.initAdminEnv(), "NEXTERM_ADMIN_PASSWORD", "synthetic-password-1")
	expect(t, strings.Contains(got.stdout, "初始密码: synthetic-password-1"), "stdout: %s", got.stdout)
}

func TestSystemdBootstrapFailedCreateAbortsAndRedacts(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	got := s.mustFail(s.payload("v1"), installOptions{initAdmin: true, initAdminExit: 1, adminPassword: password("synthetic-password-1")})
	expect(t, !strings.Contains(got.stdout+got.stderr, "synthetic-password-1"), "the injected password must never reach the installer output")
	expect(t, !strings.Contains(got.stdout, "初始密码") && strings.Contains(got.stderr, "super-admin bootstrap failed"), "output: %s %s", got.stdout, got.stderr)
	expect(t, !contains(s.systemctlCalls(), "enable --now nexterm-server.service"), "calls: %v", s.systemctlCalls())
	expect(t, s.exists("opt/nexterm/.install-incomplete"), "the incomplete marker must remain for the retry")
}

func TestSystemdBootstrapFailedCreateRetry(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	payload := s.payload("v1")
	failed := s.mustFail(payload, installOptions{initAdmin: true, initAdminExit: 1})
	expect(t, !strings.Contains(failed.stdout, "初始密码"), "stdout: %s", failed.stdout)
	retry := s.mustInstall(payload, installOptions{initAdmin: true, initAdminStdout: "nexterm-server: 初始密码: retry-password-12345（仅显示一次，请立即妥善保存）"})
	expect(t, strings.Contains(retry.stdout, "resuming incomplete first install") && strings.Contains(retry.stdout, "retry-password-12345"), "stdout: %s", retry.stdout)
	expect(t, !s.exists("opt/nexterm/.install-incomplete"), "the successful retry clears the marker")
	expect(t, strings.Join(s.initAdminCalls(), ",") == "init-admin,init-admin", "calls: %v", s.initAdminCalls())
	expectPasswordBeforeStart(t, retry.stdout)
}

func TestSystemdBootstrapExistingAccountSilence(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	got := s.mustInstall(s.payload("v1"), installOptions{initAdmin: true, initAdminExit: 3})
	expect(t, !strings.Contains(got.stdout, "初始密码") && strings.Contains(got.stdout, "already initialized"), "stdout: %s", got.stdout)
	expect(t, strings.Join(s.initAdminCalls(), ",") == "init-admin", "calls: %v", s.initAdminCalls())
}

func TestSystemdBootstrapNotInvokedOnUpgrade(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.mustInstall(s.payload("v1"), installOptions{initAdmin: true, initAdminStdout: "nexterm-server: 初始密码: first-install-password-123（仅显示一次）"})
	s.removeLog("init-admin.log")
	upgrade := s.mustInstall(s.payload("v2"), installOptions{active: "nexterm-server.service", initAdmin: true})
	expect(t, strings.Contains(upgrade.stdout, "mode: upgrade install") && !strings.Contains(upgrade.stdout, "初始密码"), "stdout: %s", upgrade.stdout)
	expect(t, !s.baseExists("init-admin.log"), "upgrades must never invoke the bootstrap")
}

func TestSystemdBootstrapSkippedWhenAuthOff(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	payload := s.payload("v1")
	s.editExample(payload, "NEXTERM_AUTH=on", "NEXTERM_AUTH=off")
	got := s.mustInstall(payload, installOptions{initAdmin: true})
	expect(t, !s.baseExists("init-admin.log"), "auth=off manages accounts itself; the bootstrap must not run")
	expect(t, strings.Contains(got.stdout, "manages accounts itself"), "stdout: %s", got.stdout)
}

func TestSystemdBootstrapSkippedOnStagedRootWithoutHook(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	got := s.mustInstall(s.payload("v1"), installOptions{})
	expect(t, strings.Contains(got.stdout, "skipping super-admin bootstrap (staged root"), "stdout: %s", got.stdout)
}

func TestSystemdNoEnableStillBootstraps(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	got := s.mustInstall(s.payload("v1"), installOptions{args: []string{"--no-enable"}, initAdmin: true, initAdminStdout: "nexterm-server: 初始密码: no-enable-password-12345（仅显示一次）"})
	expect(t, strings.Contains(got.stdout, "no-enable-password-12345"), "stdout: %s", got.stdout)
	expect(t, strings.Join(s.initAdminCalls(), ",") == "init-admin" && !anyPrefix(s.systemctlCalls(), "enable"), "calls: %v %v", s.initAdminCalls(), s.systemctlCalls())
}

func TestSystemdBootstrapDatabaseEnvironment(t *testing.T) {
	t.Run("empty file values override ambient", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		payload := s.payload("v1")
		s.editExample(payload, "NEXTERM_DB=sqlite", "NEXTERM_DB=\nNEXTERM_DB_DSN=\nNEXTERM_DB_PASSWORD_FILE=")
		got := s.mustInstall(payload, installOptions{initAdmin: true, ambient: map[string]string{
			"NEXTERM_DB": "postgres", "NEXTERM_DB_DSN": "postgres://db.example/nexterm", "NEXTERM_DB_PASSWORD_FILE": "/root/db-password",
		}})
		env := s.initAdminEnv()
		// The service resolves an empty NEXTERM_DB to sqlite; the child must see the same.
		expectValue(t, env, "NEXTERM_DB", "")
		expectValue(t, env, "NEXTERM_DB_DSN", "")
		expectValue(t, env, "NEXTERM_DB_PASSWORD_FILE", "")
		combined := got.stdout + got.stderr
		expect(t, !strings.Contains(combined, "postgres://db.example/nexterm") && !strings.Contains(combined, "/root/db-password"), "ambient values reached the console")
	})
	t.Run("file values win", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		payload := s.payload("v1")
		s.editExample(payload, "NEXTERM_DB=sqlite", "NEXTERM_DB=postgres\nNEXTERM_DB_DSN=postgres://db.example/nexterm")
		s.mustInstall(payload, installOptions{initAdmin: true, ambient: map[string]string{"NEXTERM_DB": "sqlite", "NEXTERM_DB_DSN": ""}})
		env := s.initAdminEnv()
		expectValue(t, env, "NEXTERM_DB", "postgres")
		expectValue(t, env, "NEXTERM_DB_DSN", "postgres://db.example/nexterm")
	})
	t.Run("ambient PG variables are scrubbed", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		payload := s.payload("v1")
		s.editExample(payload, "NEXTERM_DB=sqlite", "NEXTERM_DB=postgres\nNEXTERM_DB_DSN=postgres://nexterm@db.example/nexterm")
		got := s.mustInstall(payload, installOptions{initAdmin: true, ambient: map[string]string{
			"PGDATABASE": "ambientdb", "PGPASSWORD": "ambient-secret", "PGSSLMODE": "disable",
			"PGHOST": "ambient.example", "PGPORT": "5433", "PGUSER": "ambient",
		}})
		env := s.initAdminEnv()
		expectValue(t, env, "NEXTERM_DB", "postgres")
		expectValue(t, env, "NEXTERM_DB_DSN", "postgres://nexterm@db.example/nexterm")
		for _, key := range []string{"PGDATABASE", "PGPASSWORD", "PGSSLMODE", "PGHOST", "PGPORT", "PGUSER"} {
			expectValue(t, env, key, "")
		}
		expectValue(t, env, "HOME", "/var/lib/nexterm")
		expect(t, !strings.Contains(got.stdout+got.stderr, "ambient-secret"), "ambient secret reached the console")
	})
	t.Run("file PG variables pass through", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		payload := s.payload("v1")
		s.editExample(payload, "NEXTERM_DB=sqlite", "NEXTERM_DB=postgres\nNEXTERM_DB_DSN=postgres://nexterm@db.example/nexterm\nPGDATABASE=fromfile\nPGSSLMODE=verify-full\nPGHOST=pg.internal")
		s.mustInstall(payload, installOptions{initAdmin: true, ambient: map[string]string{"PGDATABASE": "ambientdb", "PGSSLMODE": "disable", "PGHOST": "ambient.example"}})
		env := s.initAdminEnv()
		expectValue(t, env, "PGDATABASE", "fromfile")
		expectValue(t, env, "PGSSLMODE", "verify-full")
		expectValue(t, env, "PGHOST", "pg.internal")
	})
	t.Run("single character keys", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		payload := s.payload("v1")
		s.editExample(payload, "NEXTERM_DB=sqlite", "NEXTERM_DB=postgres\nNEXTERM_DB_DSN=postgres://nexterm@db.example/nexterm\nA=fromfile\nB=two\nA-B=not-an-identifier\n=nope\n1BAD=digit-start\nAFTER-TRAILING=bad")
		s.mustInstall(payload, installOptions{initAdmin: true, ambient: map[string]string{"A": "ambient", "B": "ambient-two"}})
		env := s.initAdminEnv()
		expectValue(t, env, "A", "fromfile")
		expectValue(t, env, "B", "two")
	})
}

func TestSystemdBootstrapExit4KeepsInstallFailed(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	payload := s.payload("v1")
	s.mustFail(payload, installOptions{initAdmin: true, initAdminExit: 1})
	expect(t, s.exists("opt/nexterm/.install-incomplete"), "the failed create keeps the incomplete marker")
	blocked := s.mustFail(payload, installOptions{initAdmin: true, initAdminExit: 4})
	expect(t, strings.Contains(blocked.stderr, "super-admin bootstrap found an already-issued one-time init code"), "stderr: %s", blocked.stderr)
	expect(t, !strings.Contains(blocked.stdout, "初始密码") && s.exists("opt/nexterm/.install-incomplete"), "exit 4 must keep the install failed")
	expect(t, !contains(s.systemctlCalls(), "enable --now nexterm-server.service"), "the service must not start on an exit-4 run: %v", s.systemctlCalls())
	retry := s.mustInstall(payload, installOptions{initAdmin: true, initAdminStdout: "nexterm-server: 初始密码: recovered-password-12345（仅显示一次）"})
	expect(t, strings.Contains(retry.stdout, "recovered-password-12345") && !s.exists("opt/nexterm/.install-incomplete"), "stdout: %s", retry.stdout)
}

func TestSystemdBootstrapNeverUsesEnvArgv(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	writeFile(t, filepath.Join(s.stubs, "env"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$ENV_ARGV_LOG\"\nsleep 5\nexec "+lookPath(t, "env")+" \"$@\"\n", 0o755)
	got := s.mustInstall(s.payload("v1"), installOptions{
		initAdmin: true, adminPassword: password("synthetic-password-1"),
		initAdminStdout: "nexterm-server: 初始密码: synthetic-password-1（仅显示一次）",
		ambient:         map[string]string{"ENV_ARGV_LOG": filepath.Join(s.base, "env-argv.log")},
	})
	expect(t, !s.baseExists("env-argv.log"), "the bootstrap must not pass secrets through an env argv (world-readable via /proc)")
	expect(t, strings.Contains(got.stdout, "初始密码: synthetic-password-1"), "stdout: %s", got.stdout)
	env := s.initAdminEnv()
	expectValue(t, env, "NEXTERM_ADMIN_PASSWORD", "synthetic-password-1")
	expectValue(t, env, "NEXTERM_DB", "sqlite")
}

func procCmdlineLeaks(needles ...string) []string {
	entries, _ := os.ReadDir("/proc")
	var hits []string
	for _, entry := range entries {
		if strings.Trim(entry.Name(), "0123456789") != "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		for _, needle := range needles {
			if strings.Contains(string(data), needle) {
				hits = append(hits, "pid "+entry.Name()+": "+needle)
			}
		}
	}
	return hits
}

// waitForFile polls for a file written by a running process, failing fast when the process exits.
func waitForFile(path string, done <-chan struct{}, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		select {
		case <-done:
			_, err := os.Stat(path)
			return err == nil
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

func TestSystemdBootstrapSecretsNeverInProcCmdline(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc")
	}
	s := newSandbox(t, stubOptions{})
	cmd := s.installCommand(s.payload("v1"), installOptions{
		initAdmin: true, initAdminBlock: "8", adminPassword: password("synthetic-password-1"),
		initAdminStdout: "nexterm-server: 初始密码: synthetic-password-1（仅显示一次）",
	})
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			cmd.Process.Kill()
			<-done
		}
	})
	if !waitForFile(filepath.Join(s.base, "init-admin-env.log"), done, 30*time.Second) {
		t.Fatalf("the blocking bootstrap stub never started: %s", stderr.String())
	}
	var leaks []string
	for range 10 {
		leaks = append(leaks, procCmdlineLeaks("synthetic-password-1", "NEXTERM_ADMIN_PASSWORD")...)
		time.Sleep(200 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("installer did not finish")
	}
	expect(t, len(leaks) == 0, "bootstrap secrets leaked into /proc cmdline: %v", leaks)
	expect(t, waitErr == nil, "installer failed: %v %s", waitErr, stderr.String())
	expect(t, strings.Contains(stdout.String(), "初始密码: synthetic-password-1"), "stdout: %s", stdout.String())
	expectValue(t, s.initAdminEnv(), "NEXTERM_ADMIN_PASSWORD", "synthetic-password-1")
}

func TestSystemdBootstrapRunsInServiceWorkingDirectory(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	payload := s.payload("v1")
	s.editExample(payload, "NEXTERM_DB=sqlite", "NEXTERM_DB=postgres\nNEXTERM_DB_DSN=postgres://nexterm@db.example/nexterm\nNEXTERM_DB_PASSWORD_FILE=db.password")
	// The relative password file exists only inside the staged root, where the service would see it.
	s.write("var/lib/nexterm/db.password", "file-password\n")
	s.mustInstall(payload, installOptions{initAdmin: true})
	got, _ := filepath.EvalSymlinks(s.initAdminEnv()["PWD"])
	want, _ := filepath.EvalSymlinks(s.path("var/lib/nexterm"))
	expect(t, got == want, "the bootstrap must run in the service's WorkingDirectory: %q != %q", got, want)
}
