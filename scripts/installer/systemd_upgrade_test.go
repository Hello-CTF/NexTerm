//go:build !windows

package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdFirstInstallSafeDefaults(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	payload := s.payload("v1")
	got := s.mustInstall(payload, installOptions{})
	expect(t, strings.Contains(got.stdout, "mode: first install"), "stdout: %s", got.stdout)
	expect(t, s.read("opt/nexterm/nexterm-server") == readPath(t, filepath.Join(payload, "nexterm-server")), "server binary not installed")
	for _, helper := range helperNames {
		expect(t, s.read("opt/nexterm/"+helper) == readPath(t, filepath.Join(payload, helper)), "%s not installed", helper)
		expect(t, s.mode("opt/nexterm/"+helper) == 0o755, "%s mode %o", helper, s.mode("opt/nexterm/"+helper))
	}
	expect(t, s.read("opt/nexterm/web/index.html") == "<html>nexterm web v1</html>\n", "web root not installed")
	for _, unit := range serverUnits {
		expect(t, s.read("etc/systemd/system/"+unit) == systemdSource(t, unit), "%s not installed", unit)
	}
	for _, env := range envFiles {
		expect(t, s.mode("etc/nexterm/"+env) == 0o600, "%s mode %o", env, s.mode("etc/nexterm/"+env))
	}
	values := s.envValues("etc/nexterm/nexterm.env")
	expectValue(t, values, "NEXTERM_LISTEN", "127.0.0.1:8080")
	expectValue(t, values, "NEXTERM_AUTH", "on")
	expectValue(t, values, "NEXTERM_DATA_DIR", "/var/lib/nexterm")
	expectValue(t, values, "NEXTERM_WEB_ROOT", "/opt/nexterm/web")
	expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/etc/nexterm/master.key")
	expect(t, s.read("etc/nexterm/master.key") == fakeKeyHex+"\n", "master key content")
	expect(t, s.mode("etc/nexterm/master.key") == 0o600, "master key mode %o", s.mode("etc/nexterm/master.key"))
	expect(t, s.mode("var/lib/nexterm") == 0o700, "data dir mode %o", s.mode("var/lib/nexterm"))
	calls := s.systemctlCalls()
	expect(t, contains(calls, "daemon-reload") && contains(calls, "enable --now nexterm-server.service") && !anyPrefix(calls, "restart"), "calls: %v", calls)
	expect(t, !s.baseExists("useradd.log"), "useradd must not run for a staged root")
}

func TestSystemdUpgradePreservesEnvAndMasterKey(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	custom := "NEXTERM_LISTEN=0.0.0.0:9000\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_MASTER_KEY_FILE=/etc/nexterm/master.key\nNEXTERM_DB=sqlite\n"
	s.write("etc/nexterm/nexterm.env", custom)
	s.write("etc/nexterm/master.key", "preexisting-key-material\n")
	s.removeLog("systemctl.log")
	s.removeLog("openssl.log")
	payload := s.payload("v2")
	got := s.mustInstall(payload, installOptions{active: "nexterm-server.service"})
	expect(t, strings.Contains(got.stdout, "mode: upgrade install"), "stdout: %s", got.stdout)
	expect(t, s.read("opt/nexterm/nexterm-server") == readPath(t, filepath.Join(payload, "nexterm-server")), "binary not replaced")
	expect(t, s.read("opt/nexterm/web/index.html") == "<html>nexterm web v2</html>\n", "web not replaced")
	expect(t, s.read("etc/nexterm/nexterm.env") == custom, "env was rewritten")
	expect(t, s.read("etc/nexterm/master.key") == "preexisting-key-material\n", "master key was rewritten")
	expect(t, len(s.opensslCalls()) == 0, "openssl must not run when master.key exists")
	calls := s.systemctlCalls()
	expect(t, contains(calls, "daemon-reload") && contains(calls, "restart nexterm-server.service") && !anyPrefix(calls, "enable"), "calls: %v", calls)
	expect(t, strings.Contains(s.read("opt/nexterm/.rollback/nexterm-server"), "v1"), "backup must hold the v1 binary")
}

func TestSystemdUpgradeMigratesLegacyUnits(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	s.write("etc/systemd/system/nexterm-server.service", legacyUnit)
	s.write("usr/lib/systemd/system/nexterm-server.service", legacyUnit)
	got := s.mustInstall(s.payload("v2"), installOptions{})
	expect(t, strings.Contains(got.stdout, "migrating legacy unit nexterm-server.service"), "stdout: %s", got.stdout)
	expect(t, s.read("etc/systemd/system/nexterm-server.service") == systemdSource(t, "nexterm-server.service"), "unit not replaced")
	expect(t, !s.exists("usr/lib/systemd/system/nexterm-server.service"), "legacy /usr/lib copy not removed")
	expect(t, s.read("opt/nexterm/.rollback/units/etc/systemd/system/nexterm-server.service") == legacyUnit, "etc unit not backed up")
	expect(t, s.read("opt/nexterm/.rollback/units/usr/lib/systemd/system/nexterm-server.service") == legacyUnit, "lib unit not backed up")
}

func TestSystemdUpgradeWithoutEnvWritesSafeDefaults(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedLegacyBinary()
	got := s.mustInstall(s.payload("v2"), installOptions{})
	expect(t, strings.Contains(got.stdout, "mode: upgrade install"), "stdout: %s", got.stdout)
	values := s.envValues("etc/nexterm/nexterm.env")
	expectValue(t, values, "NEXTERM_LISTEN", "127.0.0.1:8080")
	expectValue(t, values, "NEXTERM_AUTH", "on")
	expectValue(t, values, "NEXTERM_DATA_DIR", "/var/lib/nexterm")
	expectValue(t, values, "NEXTERM_WEB_ROOT", "/opt/nexterm/web")
	expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/etc/nexterm/master.key")
	expect(t, s.read("etc/nexterm/master.key") == fakeKeyHex+"\n", "key not generated")
	expect(t, strings.Contains(got.stdout, "was missing and has been regenerated"), "stdout: %s", got.stdout)
	expect(t, s.read("etc/systemd/system/nexterm-server.service") == systemdSource(t, "nexterm-server.service"), "unit not installed")
}

func TestSystemdUpgradeBackfillsMissingEnvKeys(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedLegacyBinary()
	s.write("etc/nexterm/nexterm.env", "NEXTERM_DB=postgres\nNEXTERM_LISTEN=0.0.0.0:9000\n")
	s.mustInstall(s.payload("v2"), installOptions{})
	values := s.envValues("etc/nexterm/nexterm.env")
	expectValue(t, values, "NEXTERM_LISTEN", "0.0.0.0:9000")
	expectValue(t, values, "NEXTERM_DB", "postgres")
	expectValue(t, values, "NEXTERM_AUTH", "on")
	// The previous fallback location is pinned, not the example default.
	expectValue(t, values, "NEXTERM_DATA_DIR", "/var/lib/nexterm/data")
	expectValue(t, values, "NEXTERM_WEB_ROOT", "/opt/nexterm/web")
	expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/etc/nexterm/master.key")
	only := s.envValues("etc/nexterm/onlyserver.env")
	expectValue(t, only, "NEXTERM_LISTEN", "127.0.0.1:8080")
	expectValue(t, only, "NEXTERM_AUTH", "on")
}

func TestSystemdUpgradePreservesDataDirMasterKey(t *testing.T) {
	t.Run("after first install", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
		s.write("etc/nexterm/onlyserver.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_DB=sqlite\n")
		s.remove("etc/nexterm/master.key")
		s.write("var/lib/nexterm/master.key", "data-dir-key-material\n")
		s.removeLog("openssl.log")
		s.mustInstall(s.payload("v2"), installOptions{})
		expectDataDirKeyAdopted(t, s, "/var/lib/nexterm/master.key")
	})
	t.Run("legacy without env", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedLegacyBinary()
		s.write("var/lib/nexterm/master.key", "data-dir-key-material\n")
		s.mustInstall(s.payload("v2"), installOptions{})
		expectDataDirKeyAdopted(t, s, "/var/lib/nexterm/master.key")
	})
}

func expectDataDirKeyAdopted(t *testing.T, s *sandbox, keyPath string) {
	t.Helper()
	expect(t, s.read(strings.TrimPrefix(keyPath, "/")) == "data-dir-key-material\n", "data-dir key must be untouched")
	expect(t, !s.exists("etc/nexterm/master.key"), "no new /etc key may be generated")
	expect(t, len(s.opensslCalls()) == 0, "no key generation while a data-dir key exists")
	for _, env := range envFiles {
		expectValue(t, s.envValues("etc/nexterm/"+env), "NEXTERM_MASTER_KEY_FILE", keyPath)
	}
}

func TestSystemdUpgradePreservesLegacyKeyFileSetting(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_MASTER_KEY_FILE=/srv/keys/nexterm.key\nNEXTERM_DB=sqlite\n")
	s.write("srv/keys/nexterm.key", "legacy-key-material\n")
	s.removeLog("openssl.log")
	s.mustInstall(s.payload("v2"), installOptions{})
	expect(t, s.read("srv/keys/nexterm.key") == "legacy-key-material\n", "legacy key file must be untouched")
	expect(t, len(s.opensslCalls()) == 0, "no key generation while the configured key exists")
	expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/srv/keys/nexterm.key")
}

func TestSystemdUpgradeMigratesLegacyNonDefaultFlags(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	s.write("etc/systemd/system/nexterm-server.service", legacyUnitNonDefault)
	s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\n")
	s.write("srv/nexterm-data/master.key", "srv-data-key\n")
	s.removeLog("openssl.log")
	s.mustInstall(s.payload("v2"), installOptions{})
	expectNonDefaultMigrated(t, s)
	expect(t, len(s.opensslCalls()) == 0, "no key generation while the migrated data-dir key exists")
	expect(t, s.read("etc/systemd/system/nexterm-server.service") == systemdSource(t, "nexterm-server.service"), "unit not replaced")
}

func expectNonDefaultMigrated(t *testing.T, s *sandbox) {
	t.Helper()
	values := s.envValues("etc/nexterm/nexterm.env")
	expectValue(t, values, "NEXTERM_LISTEN", "0.0.0.0:9000")
	expectValue(t, values, "NEXTERM_AUTH", "off")
	expectValue(t, values, "NEXTERM_DATA_DIR", "/srv/nexterm-data")
	expectValue(t, values, "NEXTERM_WEB_ROOT", "/srv/nexterm-web")
	expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/srv/nexterm-data/master.key")
	expect(t, s.read("srv/nexterm-data/master.key") == "srv-data-key\n", "migrated data-dir key must be adopted, not regenerated")
}

func TestSystemdBackfillWithoutTrailingNewline(t *testing.T) {
	t.Run("final comment", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		s.write("etc/nexterm/nexterm.env", "NEXTERM_DB=sqlite\n# local settings")
		s.mustInstall(s.payload("v2"), installOptions{})
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_LISTEN", "127.0.0.1:8080")
		expectValue(t, values, "NEXTERM_DB", "sqlite")
		lines := strings.Split(s.read("etc/nexterm/nexterm.env"), "\n")
		expect(t, contains(lines, "# local settings") && contains(lines, "NEXTERM_LISTEN=127.0.0.1:8080"), "lines: %v", lines)
	})
	t.Run("final value", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		s.write("etc/nexterm/nexterm.env", "NEXTERM_DB=sqlite")
		s.mustInstall(s.payload("v2"), installOptions{})
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_DB", "sqlite")
		expectValue(t, values, "NEXTERM_LISTEN", "127.0.0.1:8080")
	})
}

func TestSystemdRollbackRestoresPreviousPayload(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	v1 := s.seedFirstInstall()
	s.write("etc/systemd/system/nexterm-server.service", legacyUnit)
	s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=0.0.0.0:9000\n")
	v2 := s.payload("v2")
	s.mustInstall(v2, installOptions{active: "nexterm-server.service"})
	expect(t, s.read("opt/nexterm/nexterm-server") != readPath(t, filepath.Join(v1, "nexterm-server")), "upgrade did not replace the binary")
	envAfterUpgrade := s.read("etc/nexterm/nexterm.env")
	expect(t, contains(strings.Split(envAfterUpgrade, "\n"), "NEXTERM_LISTEN=127.0.0.1:8080"), "legacy unit flags migrate into the env during the upgrade")
	s.removeLog("systemctl.log")
	s.mustInstall(v2, installOptions{args: []string{"--rollback"}, active: "nexterm-server.service"})
	expect(t, s.read("opt/nexterm/nexterm-server") == readPath(t, filepath.Join(v1, "nexterm-server")), "binary not restored")
	expect(t, s.read("etc/systemd/system/nexterm-server.service") == legacyUnit, "unit not restored")
	expect(t, s.read("etc/nexterm/nexterm.env") == envAfterUpgrade, "env must not be touched by rollback")
	expect(t, s.read("etc/nexterm/master.key") == fakeKeyHex+"\n", "master key must not be touched")
	calls := s.systemctlCalls()
	expect(t, contains(calls, "daemon-reload") && contains(calls, "restart nexterm-server.service"), "calls: %v", calls)
	expect(t, s.exists("opt/nexterm/.rollback.failed/nexterm-server"), "replaced payload must be kept")
	expect(t, !s.exists("opt/nexterm/.rollback"), "backup must be consumed")
	again := s.mustFail(v2, installOptions{args: []string{"--rollback"}})
	expect(t, strings.Contains(again.stderr, "no rollback backup"), "stderr: %s", again.stderr)
}

func TestSystemdFailureInjectionLeavesPreviousPayloadIntact(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	v1 := s.seedFirstInstall()
	v2 := s.payload("v2")
	failed := s.mustFail(v2, installOptions{failAt: "binaries"})
	expect(t, strings.Contains(failed.stderr, "injected failure at step: binaries"), "stderr: %s", failed.stderr)
	expect(t, s.read("opt/nexterm/nexterm-server") == readPath(t, filepath.Join(v1, "nexterm-server")), "binary must be untouched")
	expect(t, s.exists("opt/nexterm/.rollback/nexterm-server"), "backup must exist before mutation")

	s.mustFail(v2, installOptions{failAt: "units"})
	expect(t, s.read("opt/nexterm/nexterm-server") == readPath(t, filepath.Join(v2, "nexterm-server")), "binaries are replaced before units")
	expect(t, s.read("etc/systemd/system/nexterm-server.service") == systemdSource(t, "nexterm-server.service"), "unit not installed")
	expect(t, s.read("etc/nexterm/master.key") == fakeKeyHex+"\n", "master key must be intact")

	s.mustInstall(v2, installOptions{args: []string{"--rollback"}})
	expect(t, s.read("opt/nexterm/nexterm-server") == readPath(t, filepath.Join(v1, "nexterm-server")), "rollback must restore v1")
}

func TestSystemdFailedMasterKeyRetryPreservesDataDirKey(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedLegacyBinary()
	s.write("var/lib/nexterm/master.key", "data-dir-key-material\n")
	v2 := s.payload("v2")
	s.mustFail(v2, installOptions{failAt: "masterkey"})
	expect(t, s.exists("etc/nexterm/nexterm.env"), "the failed attempt already installed the example env")
	expect(t, !s.exists("etc/nexterm/master.key"), "the failed attempt must not generate a key")
	retry := s.mustInstall(v2, installOptions{})
	expect(t, strings.Contains(retry.stdout, "preserving rollback backup"), "stdout: %s", retry.stdout)
	expectDataDirKeyAdopted(t, s, "/var/lib/nexterm/master.key")
}

func TestSystemdFailedEnvRetryRecoversLegacyValues(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedLegacyBinary()
	s.write("etc/systemd/system/nexterm-server.service", legacyUnitNonDefault)
	s.write("srv/nexterm-data/master.key", "srv-data-key\n")
	v2 := s.payload("v2")
	s.mustFail(v2, installOptions{failAt: "env"})
	expect(t, s.read("etc/systemd/system/nexterm-server.service") == systemdSource(t, "nexterm-server.service"), "the failed attempt already replaced the unit")
	retry := s.mustInstall(v2, installOptions{})
	expect(t, strings.Contains(retry.stdout, "recovering legacy unit settings"), "stdout: %s", retry.stdout)
	expectNonDefaultMigrated(t, s)
}

func TestSystemdQuotedDataDirAndDuplicateAssignments(t *testing.T) {
	t.Run("last assignment wins", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/bogus\nNEXTERM_DATA_DIR=\"/srv/nexterm-data\"\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
		s.write("srv/nexterm-data/master.key", "quoted-key\n")
		s.mustInstall(s.payload("v2"), installOptions{})
		expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/srv/nexterm-data/master.key")
		expect(t, s.read("srv/nexterm-data/master.key") == "quoted-key\n", "quoted data-dir key must be adopted")
	})
	t.Run("unclosed quote", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=\"/srv/nexterm-data\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
		s.mustInstall(s.payload("v2"), installOptions{})
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_DATA_DIR", "\"/srv/nexterm-data")
		// An unclosed quote resolves per systemd semantics (content after the opening quote).
		expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/etc/nexterm/master.key")
	})
	for _, rawLine := range []string{
		`NEXTERM_DATA_DIR="/srv/nexterm-data" trailing`, // text after a closing quote concatenates
		`NEXTERM_DATA_DIR=/srv/nexterm\-data`,           // unquoted escapes drop the backslash
		`NEXTERM_DATA_DIR="/srv/a" "/srv/b"`,            // quote concatenation joins into one value
	} {
		t.Run(rawLine, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			s.seedFirstInstall()
			keyBefore := s.read("etc/nexterm/master.key")
			s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"+rawLine+"\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
			s.mustInstall(s.payload("v2"), installOptions{})
			expect(t, s.read("etc/nexterm/master.key") == keyBefore, "the seed key must be preserved")
			_, raw, _ := strings.Cut(rawLine, "=")
			expect(t, strings.Contains(s.read("etc/nexterm/nexterm.env"), raw), "the raw value must be preserved byte for byte")
			expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/etc/nexterm/master.key")
		})
	}
}

func TestSystemdLowerPriorityLegacyUnitDoesNotOverrideEtc(t *testing.T) {
	t.Run("etc unit is current", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:9000\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_MASTER_KEY_FILE=/etc/nexterm/master.key\nNEXTERM_DB=sqlite\n")
		s.write("usr/lib/systemd/system/nexterm-server.service", legacyUnitNonDefault)
		got := s.mustInstall(s.payload("v2"), installOptions{})
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_LISTEN", "127.0.0.1:9000")
		expectValue(t, values, "NEXTERM_AUTH", "on")
		expect(t, !s.exists("usr/lib/systemd/system/nexterm-server.service"), "lower-priority copy must still be removed")
		expect(t, !strings.Contains(got.stdout, "migrating legacy unit"), "lower-priority legacy values must not be applied")
		expect(t, s.read("opt/nexterm/.rollback/units/usr/lib/systemd/system/nexterm-server.service") == legacyUnitNonDefault, "lib unit not backed up")
	})
	t.Run("both legacy", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		s.write("etc/systemd/system/nexterm-server.service", legacyUnitNonDefault)
		other := strings.NewReplacer("0.0.0.0:9000", "0.0.0.0:9001", "/srv/nexterm-data", "/srv/other-data").Replace(legacyUnitNonDefault)
		s.write("usr/lib/systemd/system/nexterm-server.service", other)
		s.mustInstall(s.payload("v2"), installOptions{})
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_LISTEN", "0.0.0.0:9000")
		expectValue(t, values, "NEXTERM_DATA_DIR", "/srv/nexterm-data")
		expect(t, !s.exists("usr/lib/systemd/system/nexterm-server.service"), "lower-priority copy must still be removed")
	})
}

func TestSystemdFirstInstallMasterKeyRetryPreservesDataDirKey(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.write("var/lib/nexterm/master.key", "data-dir-key-material\n")
	payload := s.payload("v1")
	s.mustFail(payload, installOptions{failAt: "masterkey"})
	expect(t, s.exists("etc/nexterm/nexterm.env"), "the failed attempt already installed the example env")
	expect(t, !s.exists("etc/nexterm/master.key"), "the failed attempt must not generate a key")
	s.removeLog("systemctl.log")
	retry := s.mustInstall(payload, installOptions{})
	expect(t, strings.Contains(retry.stdout, "resuming incomplete first install"), "stdout: %s", retry.stdout)
	expectDataDirKeyAdopted(t, s, "/var/lib/nexterm/master.key")
	expect(t, contains(s.systemctlCalls(), "enable --now nexterm-server.service"), "the resumed first install must enable and start the unit")
}

func TestSystemdLegacyMasterKeyFileFlagMigratesWithPrecedence(t *testing.T) {
	seed := func(s *sandbox) {
		s.seedLegacyBinary()
		s.write("etc/systemd/system/nexterm-server.service", legacyUnitKeyFile)
		s.write("srv/nexterm-data/master.key", "data-dir-key\n")
		s.write("srv/keys/legacy.key", "explicit-legacy-key\n")
	}
	expectMigrated := func(t *testing.T, s *sandbox) {
		expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/srv/keys/legacy.key")
		expect(t, s.read("srv/keys/legacy.key") == "explicit-legacy-key\n", "explicit key changed")
		expect(t, s.read("srv/nexterm-data/master.key") == "data-dir-key\n", "data-dir key changed")
	}
	t.Run("direct", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		seed(s)
		got := s.mustInstall(s.payload("v2"), installOptions{})
		expect(t, strings.Contains(got.stdout, "migrated legacy unit setting NEXTERM_MASTER_KEY_FILE=/srv/keys/legacy.key"), "stdout: %s", got.stdout)
		expectMigrated(t, s)
	})
	t.Run("retry", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		seed(s)
		v2 := s.payload("v2")
		s.mustFail(v2, installOptions{failAt: "env"})
		retry := s.mustInstall(v2, installOptions{})
		expect(t, strings.Contains(retry.stdout, "recovering legacy unit settings"), "stdout: %s", retry.stdout)
		expectMigrated(t, s)
	})
}

func TestSystemdEnvValueWhitespaceAndQuoteVariants(t *testing.T) {
	for _, variant := range []string{
		"NEXTERM_DATA_DIR=/srv/nexterm-data   ",
		"\tNEXTERM_DATA_DIR=/srv/nexterm-data\t",
		`  NEXTERM_DATA_DIR=  "/srv/nexterm-data"  `,
		"NEXTERM_DATA_DIR='/srv/nexterm-data'",
		"NEXTERM_DATA_DIR  =/srv/nexterm-data",
		"NEXTERM_DATA_DIR\t=\t/srv/nexterm-data",
	} {
		t.Run(fmt.Sprintf("%q", variant), func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			s.seedFirstInstall()
			s.write("srv/nexterm-data/master.key", "ws-key\n")
			s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"+variant+"\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
			s.mustInstall(s.payload("v2"), installOptions{})
			expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/srv/nexterm-data/master.key")
			expect(t, s.read("srv/nexterm-data/master.key") == "ws-key\n", "key changed")
		})
	}
	t.Run("trailing text after quote", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		keyBefore := s.read("etc/nexterm/master.key")
		s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=\"/srv/nexterm-data\" trailing\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
		s.mustInstall(s.payload("v2"), installOptions{})
		expect(t, s.read("etc/nexterm/master.key") == keyBefore, "the seed key must be preserved")
		expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/etc/nexterm/master.key")
	})
}

func TestSystemdPendingRetryAndRollbackUseSavedActiveUnits(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		v2 := s.payload("v2")
		s.mustFail(v2, installOptions{failAt: "units", active: "nexterm-server.service"})
		expect(t, strings.Contains(s.read("opt/nexterm/.rollback/active-units"), "nexterm-server.service"), "active units not saved")
		s.removeLog("systemctl.log")
		retry := s.mustInstall(v2, installOptions{})
		expect(t, strings.Contains(retry.stdout, "reusing the active-unit state"), "stdout: %s", retry.stdout)
		expect(t, contains(s.systemctlCalls(), "restart nexterm-server.service"), "retry must restart the originally active unit even when it is currently inactive")
	})
	t.Run("rollback", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		v2 := s.payload("v2")
		s.mustFail(v2, installOptions{failAt: "units", active: "nexterm-server.service"})
		s.removeLog("systemctl.log")
		s.mustInstall(v2, installOptions{args: []string{"--rollback"}})
		expect(t, contains(s.systemctlCalls(), "restart nexterm-server.service"), "rollback must restart the originally active unit even when it is currently inactive")
	})
}

// TestSystemdInstallUpgradeCombinationMatrix checks key preservation, data-dir,
// enable and restart invariants across install mode, failure point, env origin,
// key location and active state.
func TestSystemdInstallUpgradeCombinationMatrix(t *testing.T) {
	keyPaths := map[string]string{
		"none": "/etc/nexterm/master.key", "fallback": "/var/lib/nexterm/data/master.key",
		"data_dir": "/var/lib/nexterm/master.key", "etc": "/etc/nexterm/master.key", "explicit": "/custom/nexterm.key",
	}
	seededKeys := map[string]string{
		"fallback": "var/lib/nexterm/data/master.key", "data_dir": "var/lib/nexterm/master.key",
		"etc": "etc/nexterm/master.key", "explicit": "custom/nexterm.key",
	}
	expectedDataDir := func(envOrigin, key string) string {
		switch {
		case envOrigin == "missing-datadir":
			return "/var/lib/nexterm/data"
		case envOrigin == "empty-datadir":
			return ""
		case envOrigin == "relative-datadir":
			return "data"
		case envOrigin == "absent" && key == "fallback":
			return "/var/lib/nexterm/data"
		}
		return "/var/lib/nexterm"
	}
	seedEnv := func(s *sandbox, envOrigin string) {
		if envOrigin == "absent" {
			return
		}
		lines := []string{"NEXTERM_LISTEN=127.0.0.1:8080", "NEXTERM_AUTH=on"}
		switch envOrigin {
		case "plain", "plain-crlf", "explicit":
			lines = append(lines, "NEXTERM_DATA_DIR=/var/lib/nexterm")
		case "empty-datadir":
			lines = append(lines, "NEXTERM_DATA_DIR=")
		case "relative-datadir":
			lines = append(lines, "NEXTERM_DATA_DIR=data")
		}
		lines = append(lines, "NEXTERM_DB=sqlite")
		if envOrigin == "explicit" {
			lines = append(lines, "NEXTERM_MASTER_KEY_FILE=/custom/nexterm.key")
		}
		ending := "\n"
		if envOrigin == "plain-crlf" {
			ending = "\r\n"
		}
		s.write("etc/nexterm/nexterm.env", strings.Join(lines, ending)+ending)
	}
	pairs := [][2]string{
		{"absent", "none"}, {"absent", "fallback"}, {"absent", "etc"},
		{"plain", "none"}, {"plain", "data_dir"}, {"plain", "etc"},
		{"plain-crlf", "data_dir"}, {"missing-datadir", "fallback"}, {"empty-datadir", "fallback"},
		{"relative-datadir", "fallback"}, {"explicit", "explicit"},
	}
	type combo struct {
		mode, failure, envOrigin, key string
		active                        bool
	}
	var combos []combo
	for _, failure := range []string{"", "env", "masterkey", "units"} {
		for _, pair := range pairs[:3] {
			combos = append(combos, combo{"first", failure, pair[0], pair[1], false})
		}
		for _, pair := range pairs {
			for _, active := range []bool{false, true} {
				combos = append(combos, combo{"upgrade", failure, pair[0], pair[1], active})
			}
		}
	}
	for _, c := range combos {
		c := c
		t.Run(fmt.Sprintf("%s/fail=%s/env=%s/key=%s/active=%t", c.mode, c.failure, c.envOrigin, c.key, c.active), func(t *testing.T) {
			t.Parallel()
			s := newSandbox(t, stubOptions{})
			if c.mode == "upgrade" {
				s.seedLegacyBinary()
			}
			seedEnv(s, c.envOrigin)
			if c.key != "none" {
				s.write(seededKeys[c.key], "seeded-"+c.key+"-key-material\n")
			}
			v2 := s.payload("v2")
			active := ""
			if c.active {
				active = "nexterm-server.service"
			}
			if c.failure == "" {
				s.mustInstall(v2, installOptions{active: active})
			} else {
				s.mustFail(v2, installOptions{failAt: c.failure, active: active})
				s.mustInstall(v2, installOptions{})
			}
			values := s.envValues("etc/nexterm/nexterm.env")
			expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", keyPaths[c.key])
			expect(t, values["NEXTERM_DATA_DIR"] == expectedDataDir(c.envOrigin, c.key), "NEXTERM_DATA_DIR = %q", values["NEXTERM_DATA_DIR"])
			if c.key != "none" {
				expect(t, s.read(seededKeys[c.key]) == "seeded-"+c.key+"-key-material\n", "seeded key must be untouched")
			} else {
				expect(t, s.read("etc/nexterm/master.key") == fakeKeyHex+"\n", "a fresh key must be generated")
			}
			calls := s.systemctlCalls()
			expect(t, contains(calls, "enable --now nexterm-server.service") == (c.mode == "first"), "enable calls: %v", calls)
			expect(t, contains(calls, "restart nexterm-server.service") == (c.mode == "upgrade" && c.active), "restart calls: %v", calls)
			expect(t, !s.exists("opt/nexterm/.install-incomplete") && !s.exists("opt/nexterm/.rollback/pending"), "markers left behind")
		})
	}
}

func TestSystemdCRLFEnvFilePreservesKey(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	s.remove("etc/nexterm/master.key")
	s.write("srv/nexterm-data/master.key", "crlf-key\n")
	s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\r\nNEXTERM_AUTH=on\r\nNEXTERM_DATA_DIR=/srv/nexterm-data\r\nNEXTERM_WEB_ROOT=/opt/nexterm/web\r\nNEXTERM_DB=sqlite\r\n")
	s.write("etc/nexterm/onlyserver.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/srv/nexterm-data\nNEXTERM_DB=sqlite\n")
	s.removeLog("openssl.log")
	s.mustInstall(s.payload("v2"), installOptions{})
	expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/srv/nexterm-data/master.key")
	expect(t, s.read("srv/nexterm-data/master.key") == "crlf-key\n", "CRLF env must resolve to the same key path systemd will use")
	expect(t, !s.exists("etc/nexterm/master.key") && len(s.opensslCalls()) == 0, "no new key may be generated")
}

func TestSystemdFailedUpgradeRetryPreservesRollback(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	v1 := s.seedFirstInstall()
	v2 := s.payload("v2")
	s.mustFail(v2, installOptions{failAt: "units"})
	v1Binary := readPath(t, filepath.Join(v1, "nexterm-server"))
	expect(t, s.read("opt/nexterm/.rollback/nexterm-server") == v1Binary, "backup must hold v1 after the failed upgrade")
	retry := s.mustInstall(v2, installOptions{})
	expect(t, strings.Contains(retry.stdout, "preserving rollback backup"), "stdout: %s", retry.stdout)
	expect(t, s.read("opt/nexterm/.rollback/nexterm-server") == v1Binary, "retry must not overwrite the original backup")
	expect(t, !s.exists("opt/nexterm/.rollback/pending"), "successful retry clears the pending marker")
	s.mustInstall(v2, installOptions{args: []string{"--rollback"}})
	expect(t, s.read("opt/nexterm/nexterm-server") == v1Binary, "rollback after a retried upgrade must restore v1")
}

func TestSystemdFirstInstallFailureAtEnvIsRecoverable(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	payload := s.payload("v1")
	failed := s.mustFail(payload, installOptions{failAt: "env"})
	expect(t, strings.Contains(failed.stderr, "injected failure at step: env"), "stderr: %s", failed.stderr)
	expect(t, !s.exists("etc/nexterm/nexterm.env"), "env step must not have run")
	s.removeLog("systemctl.log")
	retry := s.mustInstall(payload, installOptions{})
	expect(t, strings.Contains(retry.stdout, "resuming incomplete first install") && strings.Contains(retry.stdout, "mode: first install"), "stdout: %s", retry.stdout)
	expect(t, s.exists("etc/nexterm/nexterm.env"), "clean retry must install env")
	calls := s.systemctlCalls()
	expect(t, contains(calls, "daemon-reload") && contains(calls, "enable --now nexterm-server.service"), "calls: %v", calls)
	expect(t, !s.exists("opt/nexterm/.install-incomplete"), "successful retry clears the incomplete marker")
}

func TestSystemdUpgradeWithoutActiveUnitNeverRestarts(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	s.removeLog("systemctl.log")
	s.mustInstall(s.payload("v2"), installOptions{})
	calls := s.systemctlCalls()
	expect(t, contains(calls, "daemon-reload") && !anyPrefix(calls, "restart") && !anyPrefix(calls, "enable"), "calls: %v", calls)
}

func TestSystemdNoEnableAndMissingPayload(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.mustInstall(s.payload("v1"), installOptions{args: []string{"--no-enable"}})
	calls := s.systemctlCalls()
	expect(t, contains(calls, "daemon-reload") && !anyPrefix(calls, "enable"), "calls: %v", calls)

	empty := filepath.Join(s.base, "empty-payload")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	root2 := filepath.Join(s.base, "root2")
	missing := s.mustFail(empty, installOptions{root: root2})
	expect(t, strings.Contains(missing.stderr, "payload is missing"), "stderr: %s", missing.stderr)
	_, err := os.Stat(filepath.Join(root2, "opt/nexterm"))
	expect(t, os.IsNotExist(err), "failed preflight must not create directories")
}
