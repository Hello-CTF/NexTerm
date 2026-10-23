//go:build !windows

package installer

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These legs need real services or mutate the host, so they run only when the
// environment opts in; otherwise they skip with the requirement spelled out.

var realRootGuardPaths = []string{
	"/opt/nexterm", "/etc/nexterm", "/var/lib/nexterm",
	"/etc/systemd/system/nexterm-server.service", "/etc/systemd/system/nexterm-onlyserver.service",
	"/usr/lib/systemd/system/nexterm-server.service", "/usr/lib/systemd/system/nexterm-onlyserver.service",
	"/lib/systemd/system/nexterm-server.service", "/lib/systemd/system/nexterm-onlyserver.service",
}

var realRootUnitPaths = realRootGuardPaths[3:]

func realRootEnabled() bool {
	return os.Geteuid() == 0 && runtime.GOOS == "linux" && os.Getenv("NEXTERM_TEST_REAL_ROOT_INSTALL") == "1"
}

func lexists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// pristineRealRoot refuses before any mutation: the real-root legs overwrite
// or delete these paths, so they only run when every one of them is absent.
func pristineRealRoot() error {
	for _, path := range realRootGuardPaths {
		if lexists(path) {
			return fmt.Errorf("refusing the real-root leg: %s already exists; these legs mutate system paths and only run on a pristine system (disposable containers)", path)
		}
	}
	return nil
}

func cleanRealRoot(createdRunDir, createdUser bool) {
	for _, path := range []string{"/opt/nexterm", "/etc/nexterm", "/var/lib/nexterm"} {
		os.RemoveAll(path)
	}
	for _, unit := range serverUnits {
		os.Remove("/etc/systemd/system/" + unit)
	}
	if createdRunDir {
		os.RemoveAll("/run/systemd/system")
	}
	if createdUser {
		exec.Command("userdel", "nexterm").Run()
	}
}

func ensureServiceUser(t *testing.T) (created bool) {
	t.Helper()
	if exec.Command("id", "-u", "nexterm").Run() == nil {
		return false
	}
	if out, err := exec.Command("useradd", "--system", "--user-group", "--home-dir", "/var/lib/nexterm", "--shell", "/usr/sbin/nologin", "nexterm").CombinedOutput(); err != nil {
		t.Fatalf("useradd: %v %s", err, out)
	}
	return true
}

func ensureRunDir(t *testing.T) (created bool) {
	t.Helper()
	if info, err := os.Stat("/run/systemd/system"); err == nil && info.IsDir() {
		return false
	}
	if err := os.MkdirAll("/run/systemd/system", 0o755); err != nil {
		t.Fatal(err)
	}
	return true
}

func owner(t *testing.T, path string) (uint32, uint32) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	return stat.Uid, stat.Gid
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v %s", name, args, err, out)
	}
}

// realRootEnv mirrors installEnv for a real-root run without --root.
func (s *sandbox) realRootEnv(path string, extra map[string]string) []string {
	state := filepath.Join(s.base, fmt.Sprintf("systemctl-state-realroot-%d", s.runs.Add(1)))
	writeFile(s.t, state, "", 0o644)
	overrides := map[string]string{
		"PATH": path, "SYSTEMCTL_LOG": filepath.Join(s.base, "systemctl.log"), "OPENSSL_LOG": filepath.Join(s.base, "openssl.log"),
		"USERADD_LOG": filepath.Join(s.base, "useradd.log"), "NEXTERM_INSTALL_SYSTEMCTL": filepath.Join(s.stubs, "systemctl"),
		"NEXTERM_INSTALL_UNAME": filepath.Join(s.stubs, "uname"), "FAKE_ACTIVE_UNITS": "", "SYSTEMCTL_STATE": state,
	}
	for name, value := range extra {
		overrides[name] = value
	}
	return environWith(overrides)
}

func realRootBootstrapOwnership(t *testing.T) error {
	if err := pristineRealRoot(); err != nil {
		return err
	}
	service, err := user.Lookup("nexterm")
	createdUser := false
	if err != nil {
		createdUser = ensureServiceUser(t)
		if service, err = user.Lookup("nexterm"); err != nil {
			t.Fatal(err)
		}
	}
	createdRunDir := ensureRunDir(t)
	defer cleanRealRoot(createdRunDir, createdUser)

	s := newSandbox(t, stubOptions{})
	os.Chmod(s.base, 0o777)
	custom := filepath.Join(s.base, "custom-data")
	writeFile(t, filepath.Join(custom, "preexisting-top.txt"), "top\n", 0o644)
	writeFile(t, filepath.Join(custom, "nested", "nested.txt"), "nested\n", 0o644)
	run(t, "chown", "-R", "12345:12345", filepath.Join(custom, "nested"))
	run(t, "chown", "12345:12345", filepath.Join(custom, "preexisting-top.txt"))
	run(t, "chown", "nexterm:nexterm", custom)
	os.Chmod(custom, 0o755)
	payload := s.payload("v1")
	s.editExample(payload, "NEXTERM_DATA_DIR=/var/lib/nexterm", "NEXTERM_DATA_DIR="+custom)
	bootstrap := map[string]string{
		"INIT_ADMIN_LOG": filepath.Join(s.base, "init-admin.log"), "INIT_ADMIN_ENV_LOG": filepath.Join(s.base, "init-admin-env.log"),
		"NEXTERM_INSTALL_INIT_ADMIN": filepath.Join(s.stubs, "init-admin"), "NEXTERM_ADMIN_PASSWORD": "synthetic-password-1",
	}
	script := filepath.Join(repoRoot(t), "deploy", "systemd", "install.sh")

	// A missing runuser (PATH without /usr/sbin) fails closed before the service is enabled.
	blocked := exec.Command("/bin/bash", script, "--source", payload)
	blocked.Env = s.realRootEnv(s.stubs+":/bin:/usr/bin", bootstrap)
	got := runCommand(t, blocked)
	expect(t, got.code != 0 && strings.Contains(got.stderr, "runuser not found"), "missing runuser: %+v", got)
	expect(t, lexists("/opt/nexterm/.install-incomplete"), "the incomplete marker must survive")
	expect(t, !contains(s.systemctlCalls(), "enable --now nexterm-server.service"), "calls: %v", s.systemctlCalls())

	// A full run with a blocking bootstrap stub while an unprivileged reader scans /proc for argv leaks.
	bootstrap["INIT_ADMIN_TOUCH_DATA"] = "1"
	bootstrap["INIT_ADMIN_BLOCK_SECONDS"] = "8"
	cmd := exec.Command("/bin/bash", script, "--source", payload)
	cmd.Env = s.realRootEnv(s.stubs+":"+os.Getenv("PATH"), bootstrap)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	defer func() {
		select {
		case <-done:
		default:
			cmd.Process.Kill()
			<-done
		}
	}()
	if !waitForFile(filepath.Join(s.base, "init-admin-env.log"), done, 60*time.Second) {
		t.Fatal("the blocking bootstrap stub never started")
	}
	needles := filepath.Join(s.base, "proc-needles.txt")
	writeFile(t, needles, "synthetic-password-1\nNEXTERM_ADMIN_PASSWORD\n", 0o644)
	var leaks []string
	for range 10 {
		out, _ := exec.Command("runuser", "-u", "nobody", "--", "sh", "-c", `grep -a -l -F -f "$1" /proc/[0-9]*/cmdline 2>/dev/null`, "sh", needles).Output()
		if strings.TrimSpace(string(out)) != "" {
			leaks = append(leaks, strings.TrimSpace(string(out)))
		}
		time.Sleep(300 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("installer did not finish")
	}
	expect(t, len(leaks) == 0, "bootstrap secrets leaked into /proc cmdline: %v", leaks)
	expect(t, waitErr == nil, "installer failed: %v %s", waitErr, stderr.String())
	expect(t, strings.Contains(stdout.String(), "mode: first install") && strings.Contains(stdout.String(), "resuming incomplete first install"), "stdout: %s", stdout.String())
	env := s.initAdminEnv()
	expectValue(t, env, "RUNUSER_USER", "nexterm")
	expectValue(t, env, "NEXTERM_ADMIN_PASSWORD", "synthetic-password-1")
	expectValue(t, env, "NEXTERM_DATA_DIR", custom)
	expectValue(t, env, "NEXTERM_DB", "sqlite")
	uid, _ := strconv.ParseUint(service.Uid, 10, 32)
	gid, _ := strconv.ParseUint(service.Gid, 10, 32)
	dbUID, dbGID := owner(t, filepath.Join(custom, "data.db"))
	expect(t, dbUID == uint32(uid) && dbGID == uint32(gid), "the database must be born service-owned")
	for _, path := range []string{"preexisting-top.txt", "nested", "nested/nested.txt"} {
		fileUID, fileGID := owner(t, filepath.Join(custom, path))
		expect(t, fileUID == 12345 && fileGID == 12345, "pre-existing %s must keep its owner", path)
	}
	expect(t, contains(s.systemctlCalls(), "enable --now nexterm-server.service"), "calls: %v", s.systemctlCalls())
	return nil
}

func TestSystemdRealRootBootstrapPreservesCustomDataDirOwnership(t *testing.T) {
	if !realRootEnabled() {
		t.Skip("needs root on Linux plus NEXTERM_TEST_REAL_ROOT_INSTALL=1; mutates the system: disposable containers only")
	}
	if err := realRootBootstrapOwnership(t); err != nil {
		t.Fatal(err)
	}
}

type postgresTarget struct{ user, password, host, port string }

var postgresDSN = regexp.MustCompile(`^postgres(?:ql)?://([^:@/]+)(?::([^@/]*))?@([^/:]+)(?::(\d+))?/`)

func postgresFromEnv(t *testing.T) (postgresTarget, string, bool) {
	t.Helper()
	dsn, binary := os.Getenv("NEXTERM_TEST_POSTGRES_DSN"), os.Getenv("NEXTERM_TEST_SERVER_BINARY")
	if runtime.GOOS != "linux" || dsn == "" || binary == "" {
		return postgresTarget{}, "", false
	}
	if _, err := exec.LookPath("psql"); err != nil {
		return postgresTarget{}, "", false
	}
	match := postgresDSN.FindStringSubmatch(dsn)
	if match == nil {
		t.Fatalf("cannot parse NEXTERM_TEST_POSTGRES_DSN: %q", dsn)
	}
	port := match[4]
	if port == "" {
		port = "5432"
	}
	return postgresTarget{match[1], match[2], match[3], port}, binary, true
}

func (p postgresTarget) dsn(database string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s", p.user, p.password, p.host, p.port, database)
}

func (p postgresTarget) psql(database, sql string) (string, error) {
	out, err := exec.Command("psql", p.dsn(database), "-tAc", sql).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (p postgresTarget) appUsers(t *testing.T, database string) int {
	t.Helper()
	out, err := p.psql(database, "SELECT count(*) FROM app_user")
	if err != nil {
		expect(t, strings.Contains(out, "does not exist"), "psql: %s", out)
		return 0
	}
	count, _ := strconv.Atoi(out)
	return count
}

func TestSystemdBootstrapRealPostgresDatabaseIsolation(t *testing.T) {
	pg, binary, ok := postgresFromEnv(t)
	if !ok {
		t.Skip("needs Linux, psql, NEXTERM_TEST_POSTGRES_DSN and NEXTERM_TEST_SERVER_BINARY")
	}
	var created []string
	t.Cleanup(func() {
		for _, name := range created {
			pg.psql("postgres", "DROP DATABASE IF EXISTS "+name)
		}
	})
	fresh := func(name string) string {
		for _, sql := range []string{"DROP DATABASE IF EXISTS " + name, "CREATE DATABASE " + name} {
			if out, err := pg.psql("postgres", sql); err != nil {
				t.Fatalf("%s: %s", sql, out)
			}
		}
		created = append(created, name)
		return name
	}
	for _, name := range []string{"env-file PGDATABASE wins", "DSN dbname wins", "env-file PGPASSWORD wins"} {
		t.Run(name, func(t *testing.T) {
			dbA := fresh(fmt.Sprintf("nexterm_boot_a_%d", len(created)))
			dbB := fresh(fmt.Sprintf("nexterm_boot_b_%d", len(created)))
			var dsn, fileExtra string
			var ambient map[string]string
			switch name {
			case "env-file PGDATABASE wins":
				dsn, fileExtra, ambient = fmt.Sprintf("postgres://%s:%s@%s:%s/", pg.user, pg.password, pg.host, pg.port), "PGDATABASE="+dbA+"\n", map[string]string{"PGDATABASE": dbB}
			case "DSN dbname wins":
				dsn, ambient = pg.dsn(dbA), map[string]string{"PGDATABASE": dbB}
			default:
				dsn, fileExtra, ambient = fmt.Sprintf("postgres://%s@%s:%s/%s", pg.user, pg.host, pg.port, dbA), "PGPASSWORD="+pg.password+"\n", map[string]string{"PGPASSWORD": "wrong-password"}
			}
			s := newSandbox(t, stubOptions{})
			payload := s.payload("v1")
			// The real binary does not know the staged root: give it a scratch data dir and HOME.
			dataDir := filepath.Join(s.base, "data")
			s.editExample(payload, "NEXTERM_DATA_DIR=/var/lib/nexterm", "NEXTERM_DATA_DIR="+dataDir+"\nHOME="+dataDir)
			s.editExample(payload, "NEXTERM_DB=sqlite", "NEXTERM_DB=postgres\nNEXTERM_DB_DSN="+dsn+"\n"+fileExtra)
			got := s.mustInstall(payload, installOptions{ambient: ambient, initAdminCmd: binary})
			expect(t, strings.Contains(got.stdout, passwordMarker), "stdout: %s", got.stdout)
			expect(t, pg.appUsers(t, dbA) == 1, "the admin must be created in the service database")
			expect(t, pg.appUsers(t, dbB) == 0, "the admin must not be created in the ambient database")
		})
	}
}

func realRootPostgresRelativePasswordFile(t *testing.T, pg postgresTarget, binary string) error {
	if err := pristineRealRoot(); err != nil {
		return err
	}
	const database = "nexterm_boot_cwd"
	for _, sql := range []string{"DROP DATABASE IF EXISTS " + database, "CREATE DATABASE " + database} {
		if out, err := pg.psql("postgres", sql); err != nil {
			t.Fatalf("%s: %s", sql, out)
		}
	}
	createdUser := ensureServiceUser(t)
	createdRunDir := ensureRunDir(t)
	defer func() {
		pg.psql("postgres", "DROP DATABASE IF EXISTS "+database)
		cleanRealRoot(createdRunDir, createdUser)
	}()
	s := newSandbox(t, stubOptions{})
	os.Chmod(s.base, 0o777)
	payload := s.payload("v1")
	s.editExample(payload, "NEXTERM_DB=sqlite", fmt.Sprintf("NEXTERM_DB=postgres\nNEXTERM_DB_DSN=postgres://%s@%s:%s/%s\nNEXTERM_DB_PASSWORD_FILE=m50-review-db.password", pg.user, pg.host, pg.port, database))
	install := func() runResult {
		cmd := exec.Command("/bin/bash", filepath.Join(repoRoot(t), "deploy", "systemd", "install.sh"), "--source", payload)
		cmd.Dir = s.base
		cmd.Env = s.realRootEnv(s.stubs+":"+os.Getenv("PATH"), map[string]string{"NEXTERM_INSTALL_INIT_ADMIN": binary})
		return runCommand(t, cmd)
	}
	// The relative password file is absent from the service WorkingDirectory: fail closed.
	missing := install()
	expect(t, missing.code != 0 && strings.Contains(missing.stderr, "database password file") && !strings.Contains(missing.stdout, "初始密码"), "missing password file: %+v", missing)
	expect(t, lexists("/opt/nexterm/.install-incomplete"), "the incomplete marker must survive")
	expect(t, !contains(s.systemctlCalls(), "enable --now nexterm-server.service"), "calls: %v", s.systemctlCalls())

	// The file exists where the service finds it (nexterm-owned, 0600), not in the installer's cwd.
	cleanRealRoot(false, false)
	s.removeLog("systemctl.log")
	writeFile(t, "/var/lib/nexterm/m50-review-db.password", pg.password+"\n", 0o600)
	run(t, "chown", "nexterm:nexterm", "/var/lib/nexterm/m50-review-db.password")
	got := install()
	expect(t, got.code == 0 && strings.Contains(got.stdout, passwordMarker), "install: %+v", got)
	expect(t, contains(s.systemctlCalls(), "enable --now nexterm-server.service"), "calls: %v", s.systemctlCalls())
	expect(t, pg.appUsers(t, database) == 1, "the admin was not created")
	return nil
}

func TestSystemdRealRootPostgresRelativeDBPasswordFile(t *testing.T) {
	pg, binary, ok := postgresFromEnv(t)
	if !realRootEnabled() || !ok {
		t.Skip("needs root on Linux, NEXTERM_TEST_REAL_ROOT_INSTALL=1, psql, NEXTERM_TEST_POSTGRES_DSN and NEXTERM_TEST_SERVER_BINARY; mutates the system: disposable containers only")
	}
	if err := realRootPostgresRelativePasswordFile(t, pg, binary); err != nil {
		t.Fatal(err)
	}
}

func expectLegsRefuse(t *testing.T, pg postgresTarget, binary string) {
	t.Helper()
	for name, leg := range map[string]func(*testing.T) error{
		"postgres":  func(t *testing.T) error { return realRootPostgresRelativePasswordFile(t, pg, binary) },
		"ownership": realRootBootstrapOwnership,
	} {
		err := leg(t)
		expect(t, err != nil && strings.Contains(err.Error(), "refusing the real-root leg"), "%s leg did not refuse: %v", name, err)
	}
}

// sentinelRegression refuses before any write when a guarded unit path exists,
// creates sentinels exclusively and removes only what it created.
func sentinelRegression(t *testing.T, pg postgresTarget, binary string) error {
	for _, path := range realRootUnitPaths {
		if lexists(path) {
			return fmt.Errorf("the sentinel regression refuses to run: %s already exists", path)
		}
	}
	const sentinel = "sentinel: pre-existing unit, must survive\n"
	var created []string
	defer func() {
		for _, path := range created {
			os.Remove(path)
		}
	}()
	seen := map[string]bool{}
	for _, path := range realRootUnitPaths {
		real, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			continue
		}
		real = filepath.Join(real, filepath.Base(path))
		if seen[real] {
			continue
		}
		seen[real] = true
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		file.WriteString(sentinel)
		file.Close()
		created = append(created, path)
	}
	expectLegsRefuse(t, pg, binary)
	for _, path := range created {
		expect(t, readPath(t, path) == sentinel, "a leg modified the pre-existing unit file %s", path)
	}
	return nil
}

func TestSystemdRealRootLegsRefusePreexistingUnits(t *testing.T) {
	pg, binary, ok := postgresFromEnv(t)
	if !realRootEnabled() || !ok {
		t.Skip("needs root on Linux, NEXTERM_TEST_REAL_ROOT_INSTALL=1, psql, NEXTERM_TEST_POSTGRES_DSN and NEXTERM_TEST_SERVER_BINARY; mutates the system: disposable containers only")
	}
	unitFile, unitLink := realRootUnitPaths[0], realRootUnitPaths[1]
	// The sentinel regression itself must refuse a pre-existing unit and keep it byte for byte.
	const preexisting = "pre-existing unit from another install\n"
	writeFile(t, unitFile, preexisting, 0o644)
	err := sentinelRegression(t, pg, binary)
	expect(t, err != nil && strings.Contains(err.Error(), "refuses to run"), "sentinel regression did not refuse: %v", err)
	expect(t, readPath(t, unitFile) == preexisting, "the pre-existing unit was modified")
	os.Remove(unitFile)
	// A dangling unit symlink must stop both legs, preserving the link.
	const target = "/nonexistent-nexterm-sentinel-target"
	if err := os.Symlink(target, unitLink); err != nil {
		t.Fatal(err)
	}
	expectLegsRefuse(t, pg, binary)
	link, err := os.Readlink(unitLink)
	expect(t, err == nil && link == target && !lexists(target), "the dangling symlink was modified")
	os.Remove(unitLink)
	// On a pristine system the sentinel regression passes.
	if err := sentinelRegression(t, pg, binary); err != nil {
		t.Fatal(err)
	}
}

func TestSystemdRealServerArchiveSmoke(t *testing.T) {
	archive := os.Getenv("NEXTERM_TEST_SERVER_ARCHIVE")
	if archive == "" {
		candidates, _ := filepath.Glob(filepath.Join(repoRoot(t), "target", "release-assets", "nexterm-server_linux_*_*.tar.gz"))
		if len(candidates) == 0 {
			t.Skip("no packaged server archive; build one with scripts/pack-linux-server.sh or set NEXTERM_TEST_SERVER_ARCHIVE")
		}
		archive = candidates[len(candidates)-1]
	}
	s := newSandbox(t, stubOptions{noOpenSSL: true})
	extract := filepath.Join(s.base, "extracted")
	if err := os.Mkdir(extract, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, "tar", "-xzf", archive, "-C", extract)
	entries, _ := os.ReadDir(extract)
	expect(t, len(entries) == 1 && entries[0].IsDir(), "archive must extract to a single directory: %v", entries)
	payload := filepath.Join(extract, entries[0].Name())
	for _, member := range append([]string{"install.sh", "nexterm-server", "nexterm-server.service", "nexterm-onlyserver.service", "nexterm.env.example", "onlyserver.env.example", "web/index.html"}, helperNames...) {
		expect(t, lexists(filepath.Join(payload, member)), "archive payload is missing %s", member)
	}
	s.seedPlatform(s.root, "run", "", false)
	cmd := exec.Command("/bin/bash", filepath.Join(payload, "install.sh"), "--root", s.root)
	cmd.Env = s.realRootEnv(s.stubs+":"+os.Getenv("PATH"), map[string]string{"NEXTERM_INSTALL_UNAME": ""})
	got := runCommand(t, cmd)
	expect(t, got.code == 0, "install failed: %s", got.stderr)
	for _, line := range []string{"platform: Linux", "init: systemd", "(supported)", "nexterm-server.service is active"} {
		expect(t, strings.Contains(got.stdout, line), "stdout lacks %q: %s", line, got.stdout)
	}
	key := strings.TrimSpace(s.read("etc/nexterm/master.key"))
	expect(t, s.mode("etc/nexterm/master.key") == 0o600, "key mode %o", s.mode("etc/nexterm/master.key"))
	expect(t, regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(key), "a real random hex key must be generated")
	expect(t, !strings.Contains(got.stdout+got.stderr, key), "key material must never reach the console")
	expect(t, s.read("opt/nexterm/nexterm-server") == readPath(t, filepath.Join(payload, "nexterm-server")), "server binary differs")
	for _, unit := range serverUnits {
		expect(t, s.read("etc/systemd/system/"+unit) == readPath(t, filepath.Join(payload, unit)), "%s differs", unit)
	}
	expect(t, contains(s.systemctlCalls(), "enable --now nexterm-server.service"), "calls: %v", s.systemctlCalls())

	server := filepath.Join(payload, "nexterm-server")
	var probeEnv []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "NEXTERM_") {
			probeEnv = append(probeEnv, entry)
		}
	}
	probe := exec.Command(server, "--version")
	probe.Env = probeEnv
	if probe.Run() != nil {
		t.Log("skip real-binary init-admin smoke: host cannot execute the archived Linux binary")
		return
	}
	initAdmin := func(dataDir string, extra ...string) runResult {
		cmd := exec.Command(server, "init-admin", "--data-dir", dataDir)
		cmd.Env = append(append([]string{}, probeEnv...), extra...)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return runResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
	}
	dataDir := s.path("var/lib/nexterm")
	first := initAdmin(dataDir)
	expect(t, first.code == 0 && strings.Contains(first.stdout, passwordMarker), "first init-admin: %+v", first)
	initial := strings.TrimSpace(strings.SplitN(strings.SplitN(first.stdout, passwordMarker, 2)[1], "（", 2)[0])
	expect(t, initial != "" && !strings.Contains(initial, " "), "password: %q", initial)
	expect(t, strings.Contains(first.stdout, "首次登录时必须修改"), "the default random policy must force a first-login change")
	expect(t, !strings.Contains(first.stderr, initial), "the initial password must not reach stderr")
	again := initAdmin(dataDir)
	expect(t, again.code == 3 && again.stdout == "", "existing-account re-run must stay silent: %+v", again)
	supplied := filepath.Join(s.base, "supplied-data")
	failed := initAdmin(supplied, "NEXTERM_ADMIN_PASSWORD=short")
	expect(t, failed.code == 1 && failed.stdout == "", "short password: %+v", failed)
	retried := initAdmin(supplied, "NEXTERM_ADMIN_PASSWORD=synthetic-password-1")
	expect(t, retried.code == 0 && strings.Contains(retried.stdout, "初始密码: synthetic-password-1") && !strings.Contains(retried.stderr, "synthetic-password-1"), "supplied password: %+v", retried)
}
