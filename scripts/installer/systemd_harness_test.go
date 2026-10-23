//go:build !windows

package installer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// The systemd installer tests always run deploy/systemd/install.sh with --root
// into a temp directory; systemctl, uname, openssl, useradd and the init-admin
// bootstrap are stubs that record their argv, so no production service, user
// or key material is touched.

const fakeKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var (
	serverUnits = []string{"nexterm-server.service", "nexterm-onlyserver.service"}
	envFiles    = []string{"nexterm.env", "onlyserver.env"}
	helperNames = []string{
		"nexterm-helper-linux-amd64", "nexterm-helper-linux-arm64", "nexterm-helper-linux-armv7",
		"nexterm-helper-linux-riscv64", "nexterm-helper-linux-loong64",
		"nexterm-helper-darwin-amd64", "nexterm-helper-darwin-arm64",
		"nexterm-helper-windows-amd64.exe", "nexterm-helper-windows-arm64.exe",
	}
)

const legacyUnit = `[Unit]
Description=NexTerm server (legacy inline-flag layout)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
ExecStart=/opt/nexterm/nexterm-server \
  --listen 127.0.0.1:8080 \
  --auth loopback \
  --data-dir /var/lib/nexterm \
  --web-root /opt/nexterm/web
Restart=on-failure

[Install]
WantedBy=multi-user.target
`

const legacyUnitNonDefault = `[Unit]
Description=NexTerm server (legacy non-default inline flags)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
ExecStart=/opt/nexterm/nexterm-server \
  --listen 0.0.0.0:9000 \
  --auth off \
  --data-dir /srv/nexterm-data \
  --web-root /srv/nexterm-web
Restart=on-failure

[Install]
WantedBy=multi-user.target
`

const legacyUnitKeyFile = `[Unit]
Description=NexTerm server (legacy inline master-key-file)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
ExecStart=/opt/nexterm/nexterm-server \
  --listen 127.0.0.1:8080 \
  --auth on \
  --data-dir /srv/nexterm-data \
  --web-root /srv/nexterm-web \
  --master-key-file /srv/keys/legacy.key
Restart=on-failure

[Install]
WantedBy=multi-user.target
`

const systemctlStub = `#!/bin/sh
printf '%s\n' "$*" >> "$SYSTEMCTL_LOG"
cmd="${1:-}"
unit=""
for a in "$@"; do
  case "$a" in -*) ;; *) unit="$a" ;; esac
done
if [ "$cmd" = "is-active" ]; then
  if [ "${FAKE_FAIL_START:-}" = "1" ] && [ "$unit" = "nexterm-server.service" ]; then
    exit 3
  fi
  for u in $FAKE_ACTIVE_UNITS; do
    if [ "$unit" = "$u" ]; then exit 0; fi
  done
  if [ -n "${SYSTEMCTL_STATE:-}" ] && [ -f "$SYSTEMCTL_STATE" ] && grep -qx "$unit" "$SYSTEMCTL_STATE"; then
    exit 0
  fi
  exit 3
fi
if [ "$cmd" = "enable" ] || [ "$cmd" = "restart" ]; then
  if [ -n "${SYSTEMCTL_STATE:-}" ] && [ -n "$unit" ]; then
    printf '%s\n' "$unit" >> "$SYSTEMCTL_STATE"
  fi
fi
exit 0
`

const unameStub = `#!/bin/sh
printf '%s\n' "${FAKE_UNAME_NAME:-Linux}"
`

const opensslStub = `#!/bin/sh
printf '%s\n' "$*" >> "$OPENSSL_LOG"
if [ "${1:-}" = "rand" ] && [ "${2:-}" = "-hex" ]; then
  printf '%s\n' "` + fakeKeyHex + `"
  exit 0
fi
exit 1
`

const useraddStub = `#!/bin/sh
printf '%s\n' "$*" >> "$USERADD_LOG"
exit 0
`

const initAdminStub = `#!/bin/sh
printf '%s\n' "$*" >> "$INIT_ADMIN_LOG"
{
  printf 'RUNUSER_USER=%s\n' "$(id -un)"
  printf 'PWD=%s\n' "$PWD"
  printf 'NEXTERM_AUTH=%s\n' "${NEXTERM_AUTH:-}"
  printf 'NEXTERM_DATA_DIR=%s\n' "${NEXTERM_DATA_DIR:-}"
  printf 'NEXTERM_DB=%s\n' "${NEXTERM_DB:-}"
  printf 'NEXTERM_DB_DSN=%s\n' "${NEXTERM_DB_DSN:-}"
  printf 'NEXTERM_DB_PASSWORD_FILE=%s\n' "${NEXTERM_DB_PASSWORD_FILE:-}"
  printf 'NEXTERM_ADMIN_USERNAME=%s\n' "${NEXTERM_ADMIN_USERNAME:-}"
  printf 'NEXTERM_ADMIN_PASSWORD=%s\n' "${NEXTERM_ADMIN_PASSWORD:-}"
  printf 'PGDATABASE=%s\n' "${PGDATABASE:-}"
  printf 'PGPASSWORD=%s\n' "${PGPASSWORD:-}"
  printf 'PGHOST=%s\n' "${PGHOST:-}"
  printf 'PGPORT=%s\n' "${PGPORT:-}"
  printf 'PGUSER=%s\n' "${PGUSER:-}"
  printf 'PGSSLMODE=%s\n' "${PGSSLMODE:-}"
  printf 'HOME=%s\n' "${HOME:-}"
} >> "$INIT_ADMIN_ENV_LOG"
env >> "$INIT_ADMIN_ENV_LOG"
if [ "${INIT_ADMIN_TOUCH_DATA:-0}" = "1" ]; then
  : > "$NEXTERM_DATA_DIR/data.db"
fi
if [ -n "${INIT_ADMIN_STDOUT:-}" ]; then
  printf '%s\n' "$INIT_ADMIN_STDOUT"
fi
if [ -n "${INIT_ADMIN_BLOCK_SECONDS:-}" ]; then
  sleep "$INIT_ADMIN_BLOCK_SECONDS"
fi
exit "${INIT_ADMIN_EXIT:-0}"
`

const osReleaseUbuntu = "ID=ubuntu\nID_LIKE=debian\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nVERSION_ID=\"24.04\"\n"

// sandbox is one hermetic installer environment: stubs, payloads and a staged root.
type sandbox struct {
	t                 *testing.T
	base, root, stubs string
	runs              atomic.Int64
}

type stubOptions struct{ noSystemctl, noOpenSSL bool }

func newSandbox(t *testing.T, options stubOptions) *sandbox {
	t.Helper()
	base := t.TempDir()
	s := &sandbox{t: t, base: base, root: filepath.Join(base, "root"), stubs: filepath.Join(base, "stubs")}
	stubs := map[string]string{"uname": unameStub, "useradd": useraddStub, "init-admin": initAdminStub}
	if !options.noSystemctl {
		stubs["systemctl"] = systemctlStub
	}
	if !options.noOpenSSL {
		stubs["openssl"] = opensslStub
	}
	for name, content := range stubs {
		writeFile(t, filepath.Join(s.stubs, name), content, 0o755)
	}
	return s
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func systemdSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "systemd", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// payload builds an extracted release payload with fake binaries for version.
func (s *sandbox) payload(version string) string {
	s.t.Helper()
	payload := filepath.Join(s.base, "payload-"+version)
	if _, err := os.Stat(payload); err == nil {
		return payload
	}
	for _, name := range append([]string{"nexterm-server"}, helperNames...) {
		writeFile(s.t, filepath.Join(payload, name), fmt.Sprintf("#!/bin/sh\n# fake %s %s\n", name, version), 0o755)
	}
	writeFile(s.t, filepath.Join(payload, "web", "index.html"), fmt.Sprintf("<html>nexterm web %s</html>\n", version), 0o644)
	for _, unit := range serverUnits {
		writeFile(s.t, filepath.Join(payload, unit), systemdSource(s.t, unit), 0o644)
	}
	for _, env := range envFiles {
		writeFile(s.t, filepath.Join(payload, env+".example"), systemdSource(s.t, env+".example"), 0o644)
	}
	return payload
}

// editExample rewrites one of the payload's env examples.
func (s *sandbox) editExample(payload, old, replacement string) {
	s.t.Helper()
	path := filepath.Join(payload, "nexterm.env.example")
	data, err := os.ReadFile(path)
	if err != nil {
		s.t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		s.t.Fatalf("example does not contain %q", old)
	}
	writeFile(s.t, path, strings.Replace(string(data), old, replacement, 1), 0o644)
}

type installOptions struct {
	args            []string
	failAt, active  string
	uname           string
	systemd         string // run (default), systemd-link, sysv, none
	osRelease       string // empty means Ubuntu
	noOSRelease     bool
	failStart       bool
	initAdmin       bool
	initAdminExit   int
	initAdminStdout string
	initAdminBlock  string
	initAdminCmd    string
	adminPassword   *string
	ambient         map[string]string
	root            string
	extraEnv        map[string]string
}

func password(value string) *string { return &value }

func (s *sandbox) seedPlatform(root, systemd string, osRelease string, noOSRelease bool) {
	s.t.Helper()
	os.RemoveAll(filepath.Join(root, "run/systemd/system"))
	if info, err := os.Lstat(filepath.Join(root, "sbin/init")); err == nil && info.Mode()&os.ModeSymlink != 0 {
		os.Remove(filepath.Join(root, "sbin/init"))
	}
	switch systemd {
	case "", "run":
		if err := os.MkdirAll(filepath.Join(root, "run/systemd/system"), 0o755); err != nil {
			s.t.Fatal(err)
		}
	case "systemd-link", "sysv":
		target := "/lib/systemd/systemd"
		if systemd == "sysv" {
			target = "/lib/sysvinit/init"
		}
		if err := os.MkdirAll(filepath.Join(root, "sbin"), 0o755); err != nil {
			s.t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "sbin/init")); err != nil {
			s.t.Fatal(err)
		}
	}
	path := filepath.Join(root, "etc/os-release")
	if noOSRelease {
		os.Remove(path)
		return
	}
	if osRelease == "" {
		osRelease = osReleaseUbuntu
	}
	writeFile(s.t, path, osRelease, 0o644)
}

func environWith(overrides map[string]string, unset ...string) []string {
	values := map[string]string{}
	var order []string
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = value
	}
	for _, name := range unset {
		delete(values, name)
	}
	for name, value := range overrides {
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = value
	}
	env := make([]string, 0, len(values))
	for _, name := range order {
		if value, ok := values[name]; ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func (s *sandbox) installEnv(options installOptions) []string {
	s.t.Helper()
	state := filepath.Join(s.base, fmt.Sprintf("systemctl-state-%d", s.runs.Add(1)))
	writeFile(s.t, state, "", 0o644)
	overrides := map[string]string{
		"PATH":                      s.stubs + ":" + os.Getenv("PATH"),
		"SYSTEMCTL_LOG":             filepath.Join(s.base, "systemctl.log"),
		"OPENSSL_LOG":               filepath.Join(s.base, "openssl.log"),
		"USERADD_LOG":               filepath.Join(s.base, "useradd.log"),
		"FAKE_ACTIVE_UNITS":         options.active,
		"NEXTERM_INSTALL_SYSTEMCTL": filepath.Join(s.stubs, "systemctl"),
		"SYSTEMCTL_STATE":           state,
	}
	for name, value := range options.ambient {
		overrides[name] = value
	}
	unset := []string{"FAKE_UNAME_NAME", "FAKE_FAIL_START", "NEXTERM_INSTALL_FAIL_AT", "NEXTERM_INSTALL_INIT_ADMIN", "INIT_ADMIN_STDOUT", "INIT_ADMIN_BLOCK_SECONDS", "NEXTERM_ADMIN_PASSWORD"}
	if options.uname != "" {
		overrides["FAKE_UNAME_NAME"] = options.uname
	}
	if options.failStart {
		overrides["FAKE_FAIL_START"] = "1"
	}
	if options.failAt != "" {
		overrides["NEXTERM_INSTALL_FAIL_AT"] = options.failAt
	}
	switch {
	case options.initAdminCmd != "":
		overrides["NEXTERM_INSTALL_INIT_ADMIN"] = options.initAdminCmd
	case options.initAdmin:
		overrides["NEXTERM_INSTALL_INIT_ADMIN"] = filepath.Join(s.stubs, "init-admin")
		overrides["INIT_ADMIN_LOG"] = filepath.Join(s.base, "init-admin.log")
		overrides["INIT_ADMIN_ENV_LOG"] = filepath.Join(s.base, "init-admin-env.log")
		overrides["INIT_ADMIN_EXIT"] = fmt.Sprint(options.initAdminExit)
		if options.initAdminBlock != "" {
			overrides["INIT_ADMIN_BLOCK_SECONDS"] = options.initAdminBlock
		}
		if options.initAdminStdout != "" {
			overrides["INIT_ADMIN_STDOUT"] = options.initAdminStdout
		}
	}
	if options.adminPassword != nil {
		overrides["NEXTERM_ADMIN_PASSWORD"] = *options.adminPassword
	}
	for name, value := range options.extraEnv {
		overrides[name] = value
	}
	filtered := unset[:0:0]
	for _, name := range unset {
		if _, set := overrides[name]; !set {
			filtered = append(filtered, name)
		}
	}
	return environWith(overrides, filtered...)
}

func (s *sandbox) installCommand(payload string, options installOptions) *exec.Cmd {
	s.t.Helper()
	root := options.root
	if root == "" {
		root = s.root
	}
	s.seedPlatform(root, options.systemd, options.osRelease, options.noOSRelease)
	args := append([]string{filepath.Join(repoRoot(s.t), "deploy", "systemd", "install.sh"), "--root", root, "--source", payload}, options.args...)
	cmd := exec.Command("/bin/bash", args...)
	cmd.Env = s.installEnv(options)
	return cmd
}

func (s *sandbox) install(payload string, options installOptions) runResult {
	s.t.Helper()
	return runCommand(s.t, s.installCommand(payload, options))
}

func (s *sandbox) mustInstall(payload string, options installOptions) runResult {
	s.t.Helper()
	got := s.install(payload, options)
	if got.code != 0 {
		s.t.Fatalf("install failed (%d): %s", got.code, got.stderr)
	}
	return got
}

func (s *sandbox) mustFail(payload string, options installOptions) runResult {
	s.t.Helper()
	got := s.install(payload, options)
	if got.code == 0 {
		s.t.Fatalf("install unexpectedly succeeded: %s", got.stdout)
	}
	return got
}

// seedFirstInstall performs a first install of payload v1.
func (s *sandbox) seedFirstInstall() string {
	s.t.Helper()
	payload := s.payload("v1")
	s.mustInstall(payload, installOptions{})
	return payload
}

// seedLegacyBinary makes the root look like an installed older release.
func (s *sandbox) seedLegacyBinary() {
	s.write("opt/nexterm/nexterm-server", "# fake nexterm-server v0\n")
}

func (s *sandbox) path(rel string) string { return filepath.Join(s.root, filepath.FromSlash(rel)) }

func (s *sandbox) write(rel, content string) {
	s.t.Helper()
	writeFile(s.t, s.path(rel), content, 0o644)
}

func (s *sandbox) read(rel string) string {
	s.t.Helper()
	data, err := os.ReadFile(s.path(rel))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(data)
}

func readPath(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (s *sandbox) exists(rel string) bool {
	_, err := os.Lstat(s.path(rel))
	return err == nil
}

func (s *sandbox) baseExists(rel string) bool {
	_, err := os.Lstat(filepath.Join(s.base, rel))
	return err == nil
}

func (s *sandbox) remove(rel string) { os.Remove(s.path(rel)) }

func (s *sandbox) removeLog(name string) { os.Remove(filepath.Join(s.base, name)) }

func (s *sandbox) mode(rel string) os.FileMode {
	s.t.Helper()
	info, err := os.Stat(s.path(rel))
	if err != nil {
		s.t.Fatal(err)
	}
	return info.Mode().Perm()
}

func (s *sandbox) logLines(name string) []string {
	data, err := os.ReadFile(filepath.Join(s.base, name))
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func (s *sandbox) systemctlCalls() []string { return s.logLines("systemctl.log") }
func (s *sandbox) opensslCalls() []string   { return s.logLines("openssl.log") }
func (s *sandbox) initAdminCalls() []string { return s.logLines("init-admin.log") }

// initAdminEnv parses the bootstrap stub's environment log; later lines win.
func (s *sandbox) initAdminEnv() map[string]string {
	values := map[string]string{}
	data, err := os.ReadFile(filepath.Join(s.base, "init-admin-env.log"))
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, _ := strings.Cut(line, "=")
		if key != "" {
			values[key] = value
		}
	}
	return values
}

// envValues reads simple KEY=value env files, unquoting double-quoted values.
func (s *sandbox) envValues(rel string) map[string]string {
	s.t.Helper()
	values := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(s.read(rel), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			inner := value[1 : len(value)-1]
			var out strings.Builder
			for i := 0; i < len(inner); i++ {
				if inner[i] == '\\' && i+1 < len(inner) && (inner[i+1] == '\\' || inner[i+1] == '"') {
					out.WriteByte(inner[i+1])
					i++
					continue
				}
				out.WriteByte(inner[i])
			}
			value = out.String()
		}
		values[key] = value
	}
	return values
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func anyPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func expect(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

func expectValue(t *testing.T, values map[string]string, key, want string) {
	t.Helper()
	if got, ok := values[key]; !ok || got != want {
		t.Fatalf("%s = %q (present=%t), want %q; values=%v", key, got, ok, want, values)
	}
}
