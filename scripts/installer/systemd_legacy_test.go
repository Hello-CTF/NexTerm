//go:build !windows

package installer

import (
	"fmt"
	"strings"
	"testing"
)

// failures lists the attempts of a fail-closed or migration check: a direct
// run, and a retry after the env step was interrupted.
var failures = []string{"", "env"}

func runWithFailure(t *testing.T, s *sandbox, payload, failure string) runResult {
	t.Helper()
	if failure != "" {
		s.mustFail(payload, installOptions{failAt: failure})
	}
	return s.install(payload, installOptions{})
}

func legacyUnitWith(old, replacement string) string {
	if !strings.Contains(legacyUnit, old) {
		panic("legacy unit does not contain " + old)
	}
	return strings.Replace(legacyUnit, old, replacement, 1)
}

func seedLegacyUnit(s *sandbox, unit string) {
	s.seedLegacyBinary()
	s.write("etc/systemd/system/nexterm-server.service", unit)
}

// expectFailClosed checks that a rejected legacy unit is left in place and no key is generated.
func expectFailClosed(t *testing.T, s *sandbox, got runResult, message string) {
	t.Helper()
	expect(t, got.code != 0, "install must fail closed")
	expect(t, strings.Contains(got.stderr, message), "stderr: %s", got.stderr)
	expect(t, strings.Contains(s.read("etc/systemd/system/nexterm-server.service"), "legacy inline-flag layout"), "the unit must not be replaced")
	expect(t, !s.exists("etc/nexterm/master.key"), "no key may be generated")
}

func TestSystemdLegacyQuotedPathsTokenization(t *testing.T) {
	quoted := `[Unit]
Description=NexTerm server (legacy quoted paths)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
WorkingDirectory=/var/lib/nexterm
ExecStart=/opt/nexterm/nexterm-server \
  --listen 0.0.0.0:9000 \
  --auth off \
  --data-dir "/srv/nexterm data" \
  --web-root "/srv/nexterm web" \
  --master-key-file "/srv/keys/legacy key"
Restart=on-failure

[Install]
WantedBy=multi-user.target
`
	seed := func(s *sandbox, unit string) {
		seedLegacyUnit(s, unit)
		s.write("srv/nexterm data/master.key", "spaced-data-key\n")
		s.write("srv/keys/legacy key", "spaced-explicit-key\n")
	}
	expectMigrated := func(t *testing.T, s *sandbox) {
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_LISTEN", "0.0.0.0:9000")
		expectValue(t, values, "NEXTERM_AUTH", "off")
		expectValue(t, values, "NEXTERM_DATA_DIR", "/srv/nexterm data")
		expectValue(t, values, "NEXTERM_WEB_ROOT", "/srv/nexterm web")
		expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/srv/keys/legacy key")
		expect(t, s.read("srv/keys/legacy key") == "spaced-explicit-key\n" && s.read("srv/nexterm data/master.key") == "spaced-data-key\n", "keys changed")
	}
	for _, failure := range failures {
		t.Run("migrate fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seed(s, quoted)
			got := runWithFailure(t, s, s.payload("v2"), failure)
			expect(t, got.code == 0, "stderr: %s", got.stderr)
			if failure != "" {
				expect(t, strings.Contains(got.stdout, "recovering legacy unit settings"), "stdout: %s", got.stdout)
			}
			expectMigrated(t, s)
		})
	}
	t.Run("unterminated quote", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		seed(s, strings.Replace(quoted, `"/srv/keys/legacy key"`, `"/srv/keys/legacy key`, 1))
		got := s.mustFail(s.payload("v2"), installOptions{})
		expect(t, strings.Contains(got.stderr, "unsupported quoting"), "stderr: %s", got.stderr)
		expect(t, strings.Contains(s.read("etc/systemd/system/nexterm-server.service"), "legacy quoted paths"), "the unit must not be replaced when parsing fails")
		expect(t, !s.exists("etc/nexterm/master.key"), "no key may be generated after a parse failure")
	})
}

func TestSystemdMissingAndEmptyDataDirPreserveFallback(t *testing.T) {
	seedFallback := func(s *sandbox, dir, key string) {
		s.seedLegacyBinary()
		s.write(dir+"/data.db", "db-bytes\n")
		s.write(dir+"/master.key", key)
	}
	t.Run("no env", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		seedFallback(s, "var/lib/nexterm/data", "fallback-key\n")
		s.mustInstall(s.payload("v2"), installOptions{})
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_DATA_DIR", "/var/lib/nexterm/data")
		expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/var/lib/nexterm/data/master.key")
		expect(t, s.read("var/lib/nexterm/data/master.key") == "fallback-key\n" && s.read("var/lib/nexterm/data/data.db") == "db-bytes\n", "fallback data changed")
		expect(t, !s.exists("etc/nexterm/master.key") && len(s.opensslCalls()) == 0, "no new key may be generated")
	})
	t.Run("explicit empty", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		seedFallback(s, "var/lib/nexterm/data", "fallback-key\n")
		s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
		s.mustInstall(s.payload("v2"), installOptions{})
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_DATA_DIR", "")
		expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/var/lib/nexterm/data/master.key")
		expect(t, s.read("var/lib/nexterm/data/master.key") == "fallback-key\n" && !s.exists("etc/nexterm/master.key"), "fallback key not adopted")
	})
	t.Run("legacy working directory", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		unit := strings.Replace(strings.Replace(legacyUnit, "  --data-dir /var/lib/nexterm \\\n", "", 1),
			"EnvironmentFile=-/etc/nexterm/nexterm.env", "EnvironmentFile=-/etc/nexterm/nexterm.env\nWorkingDirectory=/srv/wd", 1)
		seedLegacyUnit(s, unit)
		s.write("srv/wd/data/master.key", "custom-wd-key\n")
		s.mustInstall(s.payload("v2"), installOptions{})
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_DATA_DIR", "/srv/wd/data")
		expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/srv/wd/data/master.key")
		expect(t, s.read("srv/wd/data/master.key") == "custom-wd-key\n", "key changed")
	})
}

func TestSystemdRelativeDataDirResolvedAgainstWorkingDir(t *testing.T) {
	t.Run("unchanged working directory", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		s.remove("etc/nexterm/master.key")
		s.write("var/lib/nexterm/data/master.key", "relative-key\n")
		s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=data\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
		s.write("etc/nexterm/onlyserver.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=data\nNEXTERM_DB=sqlite\n")
		s.mustInstall(s.payload("v2"), installOptions{})
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_DATA_DIR", "data")
		expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/var/lib/nexterm/data/master.key")
		expect(t, s.read("var/lib/nexterm/data/master.key") == "relative-key\n" && !s.exists("etc/nexterm/master.key"), "relative key not adopted")
	})
	t.Run("legacy working directory", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		unit := strings.Replace(strings.Replace(legacyUnit, "  --data-dir /var/lib/nexterm", "  --data-dir rel-data", 1),
			"EnvironmentFile=-/etc/nexterm/nexterm.env", "EnvironmentFile=-/etc/nexterm/nexterm.env\nWorkingDirectory=/srv/wd", 1)
		seedLegacyUnit(s, unit)
		s.write("srv/wd/rel-data/master.key", "relative-custom-wd-key\n")
		s.mustInstall(s.payload("v2"), installOptions{})
		values := s.envValues("etc/nexterm/nexterm.env")
		expectValue(t, values, "NEXTERM_DATA_DIR", "/srv/wd/rel-data")
		expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/srv/wd/rel-data/master.key")
		expect(t, s.read("srv/wd/rel-data/master.key") == "relative-custom-wd-key\n", "key changed")
	})
}

func TestSystemdUpgradePreservesRelativeServicePaths(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	for index, name := range []string{"nexterm", "onlyserver"} {
		unit := "etc/systemd/system/" + serverUnits[index]
		s.write(unit, strings.Replace(s.read(unit), "WorkingDirectory=/var/lib/nexterm", "WorkingDirectory=/srv/old-wd", 1))
		s.write("etc/nexterm/"+name+".env", "NEXTERM_DB=postgres\nNEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_WEB_ROOT=web assets\nNEXTERM_DB_PASSWORD_FILE=secrets/../db-password\nNEXTERM_MASTER_KEY_FILE=/etc/nexterm/master.key\n")
	}
	payload := s.payload("v2")
	s.mustFail(payload, installOptions{failAt: "masterkey"})
	s.mustInstall(payload, installOptions{})
	for index, name := range []string{"nexterm", "onlyserver"} {
		values := s.envValues("etc/nexterm/" + name + ".env")
		expectValue(t, values, "NEXTERM_WEB_ROOT", "/srv/old-wd/web assets")
		expectValue(t, values, "NEXTERM_DB_PASSWORD_FILE", "/srv/old-wd/db-password")
		expect(t, strings.Contains(s.read("etc/systemd/system/"+serverUnits[index]), "WorkingDirectory=/var/lib/nexterm"), "unit not replaced")
	}
}

func TestSystemdLegacyCStyleEscapes(t *testing.T) {
	escaped := `[Unit]
Description=NexTerm server (legacy C-style escapes)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
WorkingDirectory=/var/lib/nexterm
ExecStart=/opt/nexterm/nexterm-server \
  --listen 0.0.0.0:9000 \
  --auth off \
  --data-dir /srv/nexterm\sdata \
  --web-root /srv/nexterm\x20web \
  --master-key-file /srv/keys/legacy\skey
Restart=on-failure

[Install]
WantedBy=multi-user.target
`
	for _, failure := range failures {
		t.Run("migrate fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seedLegacyUnit(s, escaped)
			s.write("srv/nexterm data/master.key", "escaped-data-key\n")
			s.write("srv/keys/legacy key", "escaped-explicit-key\n")
			got := runWithFailure(t, s, s.payload("v2"), failure)
			expect(t, got.code == 0, "stderr: %s", got.stderr)
			if failure != "" {
				expect(t, strings.Contains(got.stdout, "recovering legacy unit settings"), "stdout: %s", got.stdout)
			}
			values := s.envValues("etc/nexterm/nexterm.env")
			expectValue(t, values, "NEXTERM_DATA_DIR", "/srv/nexterm data")
			expectValue(t, values, "NEXTERM_WEB_ROOT", "/srv/nexterm web")
			expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/srv/keys/legacy key")
			expect(t, s.read("srv/keys/legacy key") == "escaped-explicit-key\n" && s.read("srv/nexterm data/master.key") == "escaped-data-key\n", "keys changed")
		})
	}
	t.Run("unterminated quote", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		seedLegacyUnit(s, strings.Replace(escaped, `/srv/keys/legacy\skey`, `"/srv/keys/legacy key`, 1))
		got := s.mustFail(s.payload("v2"), installOptions{})
		expect(t, strings.Contains(got.stderr, "unsupported quoting"), "stderr: %s", got.stderr)
		expect(t, strings.Contains(s.read("etc/systemd/system/nexterm-server.service"), "legacy C-style escapes"), "the unit must not be replaced when parsing fails")
	})
}

func TestSystemdLegacyDuplicateFlagsLastValueWins(t *testing.T) {
	duplicate := `[Unit]
Description=NexTerm server (legacy duplicate flags)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
WorkingDirectory=/var/lib/nexterm
ExecStart=/opt/nexterm/nexterm-server \
  --listen 127.0.0.1:8080 \
  --data-dir /srv/first \
  --data-dir=/srv/last \
  --master-key-file /srv/first.key \
  --master-key-file=/srv/last.key
Restart=on-failure

[Install]
WantedBy=multi-user.target
`
	for _, failure := range failures {
		t.Run("fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seedLegacyUnit(s, duplicate)
			s.write("srv/last.key", "last-key\n")
			got := runWithFailure(t, s, s.payload("v2"), failure)
			expect(t, got.code == 0, "stderr: %s", got.stderr)
			values := s.envValues("etc/nexterm/nexterm.env")
			expectValue(t, values, "NEXTERM_DATA_DIR", "/srv/last")
			expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/srv/last.key")
			expect(t, s.read("srv/last.key") == "last-key\n", "last key changed")
			expect(t, !s.exists("srv/first.key"), "the superseded first key must not be generated")
		})
	}
}

func TestSystemdPathNormalizationAndContainment(t *testing.T) {
	seedDotDot := func(s *sandbox) {
		s.seedFirstInstall()
		s.remove("etc/nexterm/master.key")
		s.write("srv/nexterm-data/master.key", "dotdot-key\n")
		s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/srv/missing/../nexterm-data\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
		s.write("etc/nexterm/onlyserver.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/srv/missing/../nexterm-data\nNEXTERM_DB=sqlite\n")
	}
	for _, failure := range []string{"", "masterkey"} {
		t.Run("dotdot fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seedDotDot(s)
			payload := s.payload("v2")
			if failure != "" {
				s.mustFail(payload, installOptions{failAt: failure})
			}
			s.mustInstall(payload, installOptions{})
			expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/srv/nexterm-data/master.key")
			expect(t, s.read("srv/nexterm-data/master.key") == "dotdot-key\n" && !s.exists("etc/nexterm/master.key"), "cleaned key path not adopted")
		})
	}
	t.Run("containment", func(t *testing.T) {
		s := newSandbox(t, stubOptions{})
		s.seedFirstInstall()
		s.remove("etc/nexterm/master.key")
		s.write("review-escaped/master.key", "contained-key\n")
		s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_MASTER_KEY_FILE=/../review-escaped/master.key\nNEXTERM_DB=sqlite\n")
		s.write("etc/nexterm/onlyserver.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_DB=sqlite\n")
		s.mustInstall(s.payload("v2"), installOptions{})
		expect(t, s.read("review-escaped/master.key") == "contained-key\n", "the escaped path must resolve inside the staged root")
		expect(t, !s.baseExists("review-escaped"), "the staged root's parent must never be touched")
		// The admin's raw value is preserved; systemd cleans it identically.
		expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/../review-escaped/master.key")
	})
}

func TestSystemdLegacyNewlineValuesFailClosed(t *testing.T) {
	for _, spelling := range []string{`legacy\nkey`, `legacy\u000Akey`} {
		for _, failure := range failures {
			t.Run(spelling+" fail="+failure, func(t *testing.T) {
				s := newSandbox(t, stubOptions{})
				seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", "  --master-key-file /srv/keys/"+spelling+"\n  --web-root /opt/nexterm/web"))
				expectFailClosed(t, s, runWithFailure(t, s, s.payload("v2"), failure), "contain a newline")
			})
		}
	}
	for _, spelling := range []string{`legacy\x0Akey`, `legacy\012key`, `legacy\rkey`} {
		for _, failure := range failures {
			t.Run(spelling+" continued fail="+failure, func(t *testing.T) {
				s := newSandbox(t, stubOptions{})
				seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", "  --master-key-file /srv/keys/"+spelling+" \\\n  --web-root /opt/nexterm/web"))
				expectFailClosed(t, s, runWithFailure(t, s, s.payload("v2"), failure), "contain a newline")
			})
		}
	}
}

// onlyserverWithLiteralKey keeps a literal \U0000FFFF path in onlyserver.env,
// which must round-trip unchanged through systemd's env-file parser.
func seedOnlyserverLiteralKey(s *sandbox) {
	s.write("etc/nexterm/onlyserver.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_DB=sqlite\n"+`NEXTERM_MASTER_KEY_FILE="/srv/keys/legacy\\U0000FFFFkey"`+"\n")
}

func TestSystemdLegacyNoncharacterEscapeRoundTrip(t *testing.T) {
	for _, failure := range failures {
		t.Run("uppercase U fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", "  --master-key-file /srv/keys/legacy\\U0000FFFFkey\n  --web-root /opt/nexterm/web"))
			s.write(`srv/keys/legacy\U0000FFFFkey`, "literal-path-key\n")
			seedOnlyserverLiteralKey(s)
			got := runWithFailure(t, s, s.payload("v2"), failure)
			expect(t, got.code == 0, "stderr: %s", got.stderr)
			expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", `/srv/keys/legacy\U0000FFFFkey`)
			expect(t, s.read(`srv/keys/legacy\U0000FFFFkey`) == "literal-path-key\n", "key changed")
			expect(t, !s.exists("etc/nexterm/master.key"), "no key may be generated at a decoded path")
		})
		t.Run("invalid hex fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", "  --web-root /srv/web\\x4Groot"))
			s.write(`srv/web\x4Groot/master.key`, "literal-path-key\n")
			seedOnlyserverLiteralKey(s)
			got := runWithFailure(t, s, s.payload("v2"), failure)
			expect(t, got.code == 0, "stderr: %s", got.stderr)
			expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_WEB_ROOT", `/srv/web\x4Groot`)
			expect(t, s.read(`srv/web\x4Groot/master.key`) == "literal-path-key\n", "key changed")
		})
	}
}

func TestSystemdLegacySpecifierValues(t *testing.T) {
	for _, failure := range failures {
		t.Run("unexpandable fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", "  --master-key-file /srv/keys/legacy%ikey\n  --web-root /opt/nexterm/web"))
			expectFailClosed(t, s, runWithFailure(t, s, s.payload("v2"), failure), "specifiers")
		})
		t.Run("escaped percent fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", "  --master-key-file /srv/keys/legacy%%key\n  --web-root /opt/nexterm/web"))
			s.write("srv/keys/legacy%key", "percent-key\n")
			got := runWithFailure(t, s, s.payload("v2"), failure)
			expect(t, got.code == 0, "stderr: %s", got.stderr)
			expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/srv/keys/legacy%key")
			expect(t, s.read("srv/keys/legacy%key") == "percent-key\n", "key changed")
		})
	}
}

func TestSystemdLegacyLowercaseUDecodesNoncharacters(t *testing.T) {
	for _, failure := range failures {
		t.Run("lowercase u fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", "  --master-key-file /srv/keys/legacy\\uFFFFkey \\\n  --web-root /opt/nexterm/web"))
			seedOnlyserverLiteralKey(s)
			// A decoded noncharacter must fail closed, not be written to an unloadable env file.
			expectFailClosed(t, s, runWithFailure(t, s, s.payload("v2"), failure), "cannot be represented in an environment file")
		})
		t.Run("uppercase U fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", "  --master-key-file /srv/keys/legacy\\U0000FFFFkey \\\n  --web-root /opt/nexterm/web"))
			seedOnlyserverLiteralKey(s)
			s.write(`srv/keys/legacy\U0000FFFFkey`, "decoded-path-key\n")
			got := runWithFailure(t, s, s.payload("v2"), failure)
			expect(t, got.code == 0, "stderr: %s", got.stderr)
			// Uppercase \U keeps noncharacters literal like systemd.
			expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", `/srv/keys/legacy\U0000FFFFkey`)
			expect(t, s.read(`srv/keys/legacy\U0000FFFFkey`) == "decoded-path-key\n" && !s.exists("etc/nexterm/master.key"), "key changed")
		})
	}
}

func TestSystemdLegacyUnrepresentableBytesFailClosed(t *testing.T) {
	for _, value := range []string{`/srv/keys/legacy￿key`, `/srv/keys/legacy\uD800key`, `/srv/keys/legacy\xFFkey`} {
		for _, failure := range failures {
			t.Run(value+" fail="+failure, func(t *testing.T) {
				s := newSandbox(t, stubOptions{})
				seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", "  --master-key-file "+value+" \\\n  --web-root /opt/nexterm/web"))
				expectFailClosed(t, s, runWithFailure(t, s, s.payload("v2"), failure), "cannot be represented in an environment file")
			})
		}
	}
}

func TestSystemdLegacyEmptyMasterKeyArgFallsBack(t *testing.T) {
	for _, failure := range failures {
		t.Run("fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", "  --data-dir /srv/empty-data \\\n  --master-key-file \"\" \\\n  --web-root /srv/web"))
			s.write("srv/empty-data/master.key", "empty-data-key\n")
			got := runWithFailure(t, s, s.payload("v2"), failure)
			expect(t, got.code == 0, "stderr: %s", got.stderr)
			values := s.envValues("etc/nexterm/nexterm.env")
			expectValue(t, values, "NEXTERM_DATA_DIR", "/srv/empty-data")
			// An empty master-key argument falls back to the data-dir key, not the next flag.
			expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/srv/empty-data/master.key")
			expect(t, s.read("srv/empty-data/master.key") == "empty-data-key\n", "key changed")
			expect(t, !s.exists("var/lib/nexterm/--web-root"), "the next flag must never become the key path")
		})
	}
}

func TestSystemdEnvCommentContinuationKeepsAssignment(t *testing.T) {
	for _, failure := range failures {
		t.Run("fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			s.seedFirstInstall()
			s.remove("etc/nexterm/master.key")
			s.write("srv/comment-data/master.key", "comment-key\n")
			s.write("etc/nexterm/nexterm.env", "# continuation comment \\\nNEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/srv/comment-data\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
			s.write("etc/nexterm/onlyserver.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/srv/comment-data\nNEXTERM_DB=sqlite\n")
			got := runWithFailure(t, s, s.payload("v2"), failure)
			expect(t, got.code == 0, "stderr: %s", got.stderr)
			// An assignment after a comment-ending backslash stays active.
			expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/srv/comment-data/master.key")
			expect(t, s.read("srv/comment-data/master.key") == "comment-key\n" && !s.exists("etc/nexterm/master.key"), "key not adopted")
		})
	}
}

func TestSystemdEmptyDataDirPinnedOnWorkingDirChange(t *testing.T) {
	for _, failure := range failures {
		t.Run("fail="+failure, func(t *testing.T) {
			s := newSandbox(t, stubOptions{})
			unit := strings.Replace(strings.Replace(legacyUnit, "  --data-dir /var/lib/nexterm \\\n", "", 1),
				"EnvironmentFile=-/etc/nexterm/nexterm.env", "EnvironmentFile=-/etc/nexterm/nexterm.env\nWorkingDirectory=/srv/old-wd", 1)
			seedLegacyUnit(s, unit)
			s.write("srv/old-wd/data/data.db", "db-bytes\n")
			s.write("srv/old-wd/data/master.key", "old-wd-key\n")
			s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n")
			s.write("etc/nexterm/onlyserver.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/srv/old-wd/data\nNEXTERM_DB=sqlite\n")
			got := runWithFailure(t, s, s.payload("v2"), failure)
			expect(t, got.code == 0, "stderr: %s", got.stderr)
			values := s.envValues("etc/nexterm/nexterm.env")
			expectValue(t, values, "NEXTERM_DATA_DIR", "/srv/old-wd/data")
			expectValue(t, values, "NEXTERM_MASTER_KEY_FILE", "/srv/old-wd/data/master.key")
			expect(t, s.read("srv/old-wd/data/master.key") == "old-wd-key\n" && s.read("srv/old-wd/data/data.db") == "db-bytes\n", "old data changed")
			expect(t, !s.exists("etc/nexterm/master.key"), "no key may be generated")
		})
	}
}

func TestSystemdLegacyEmptyMasterKeyOverrideClearsStaleEnvKey(t *testing.T) {
	layouts := map[string]string{
		"middle":   "  --data-dir /srv/data \\\n  %s \\\n  --web-root /srv/web",
		"trailing": "  --data-dir /srv/data \\\n  --web-root /srv/web \\\n  %s",
	}
	for position, layout := range layouts {
		for _, flag := range []string{`--master-key-file ""`, `--master-key-file ''`, "--master-key-file="} {
			for _, failure := range failures {
				t.Run(fmt.Sprintf("%s %s fail=%s", position, flag, failure), func(t *testing.T) {
					s := newSandbox(t, stubOptions{})
					seedLegacyUnit(s, legacyUnitWith("  --web-root /opt/nexterm/web", fmt.Sprintf(layout, flag)))
					s.write("srv/keys/env.key", "stale-env-key\n")
					s.write("srv/data/master.key", "data-dir-key\n")
					s.write("etc/nexterm/nexterm.env", "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/srv/data\nNEXTERM_WEB_ROOT=/srv/web\nNEXTERM_MASTER_KEY_FILE=/srv/keys/env.key\nNEXTERM_DB=sqlite\n")
					got := runWithFailure(t, s, s.payload("v2"), failure)
					expect(t, got.code == 0, "stderr: %s", got.stderr)
					expectValue(t, s.envValues("etc/nexterm/nexterm.env"), "NEXTERM_MASTER_KEY_FILE", "/srv/data/master.key")
					expect(t, s.read("srv/data/master.key") == "data-dir-key\n", "data-dir key changed")
					expect(t, s.read("srv/keys/env.key") == "stale-env-key\n", "the stale env key must not be adopted")
				})
			}
		}
	}
}
