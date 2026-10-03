package guard

import "testing"

func TestR6EnvSignalOptionsPreserveNestedRuling(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`env --default-signal=PIPE rm -rf /home/user`,
		`env --ignore-signal=PIPE rm -rf /home/user`,
		`env --block-signal=PIPE rm -rf /home/user`,
		`env --list-signal-handling rm -rf /home/user`,
		`env --default-signal=PIPE rm -rf /root`,
		`env --list rm -rf /home/user`,
	}
	for _, command := range forbidden {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != Forbidden {
				t.Fatalf("ClassifyCommand(%q) = %v, want Forbidden", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionDeny {
				t.Fatalf("silent decision = %+v, want ActionDeny", decision)
			}
		})
	}
	confirm := []string{
		`env --default-signal=PIPE touch /tmp/x`,
		`env --d ls`,
		`env --de ls`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
			if decision := Decide(Config{Mode: ReadOnly}, ruling, nil); decision.Action != ActionDeny {
				t.Fatalf("read-only decision = %+v, want ActionDeny", decision)
			}
		})
	}
	safe := []string{
		`env --default-signal=PIPE ls`,
		`env --def=PIPE ls`,
		`env --ignore-s=PIPE ls`,
		`env --block=PIPE ls`,
		`env --list-signal-handling ls`,
		`env --deb ls`,
		`env -v ls`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

func TestR6CurlDataAndFormAliasesAreNeverSafe(t *testing.T) {
	t.Parallel()
	confirm := []string{
		`curl --data-urlencode foo=bar https://example.test/change`,
		`curl --data-urlencode=foo=bar https://example.test/change`,
		`curl --data-ascii foo=bar https://example.test/change`,
		`curl --data-ascii=foo=bar https://example.test/change`,
		`curl --form-string foo=bar https://example.test/change`,
		`curl --form-string=foo=bar https://example.test/change`,
		`curl -Q 'DELE /x' ftp://example.test/`,
		`curl --quote 'DELE /x' ftp://example.test/`,
		`curl -G -X POST --data foo=bar https://example.test/change`,
		`curl -G --form foo=bar https://example.test/change`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
			if decision := Decide(Config{Mode: ReadOnly}, ruling, nil); decision.Action != ActionDeny {
				t.Fatalf("read-only decision = %+v, want ActionDeny", decision)
			}
		})
	}
	safe := []string{
		`curl -G --data-urlencode foo=bar https://example.test`,
		`curl --get --data foo=bar https://example.test`,
		`curl -G -d foo=bar https://example.test`,
		`curl -sXGET https://example.test`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

func TestR6RedisRemainingValueOptionsExposeCommand(t *testing.T) {
	t.Parallel()
	danger := []string{
		`redis-cli --name INFO FLUSHALL`,
		`redis-cli --quoted-pattern 'x*' FLUSHALL`,
		`redis-cli --tls-ciphers DEFAULT FLUSHALL`,
		`redis-cli --tls-ciphersuites TLS_AES_128_GCM_SHA256 FLUSHALL`,
		`redis-cli --functions-rdb /tmp/f.rdb FLUSHALL`,
		`redis-cli --key /tmp/key FLUSHALL`,
		`redis-cli --keystats-samples 10 FLUSHALL`,
		`redis-cli --top 5 FLUSHALL`,
		`redis-cli --intrinsic-latency 5 FLUSHALL`,
	}
	for _, command := range danger {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != Danger {
				t.Fatalf("ClassifyCommand(%q) = %v, want Danger", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAsk {
				t.Fatalf("silent decision = %+v, want ActionAsk", decision)
			}
			if decision := Decide(Config{Mode: ReadOnly}, ruling, nil); decision.Action != ActionDeny {
				t.Fatalf("read-only decision = %+v, want ActionDeny", decision)
			}
		})
	}
	safe := []string{
		`redis-cli --name INFO PING`,
		`redis-cli --quoted-pattern 'x*' --scan`,
		`redis-cli --tls-ciphers DEFAULT INFO`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

func TestR6DockerAliasesAndForceClusters(t *testing.T) {
	t.Parallel()
	danger := []string{
		`docker rm -f $(docker container ps -aq)`,
		`docker rm -f $(docker container ls -aq)`,
		`docker rm -f $(docker container list -aq)`,
		`docker rmi -f $(docker image ls -q)`,
		`docker rmi -f $(docker image list -q)`,
		`docker rm -fv $(docker ps -aq)`,
		`docker rm -vf $(docker ps -aq)`,
		`docker rm --force $(docker ps -aq)`,
		`docker container rm -fv $(docker container ls -aq)`,
		`docker image rm -f $(docker image list -q)`,
		`docker rm -f $(docker -H tcp://127.0.0.1:2375 container ps -aq)`,
		`docker rm -fv $(docker ps -q)`,
		`docker rm -f $(docker container ps -q)`,
		`podman rm -fv $(podman container ps -aq)`,
	}
	for _, command := range danger {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != Danger {
				t.Fatalf("ClassifyCommand(%q) = %v, want Danger", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAsk {
				t.Fatalf("silent decision = %+v, want ActionAsk", decision)
			}
		})
	}
	confirm := []string{
		`docker rm -v $(docker ps -aq)`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
}

func TestR6CurlRemoteNameAllWrites(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		`curl --remote-name-all https://example.test/file`,
		`curl --remote-name-all --remote-header-name https://example.test/file`,
	} {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
			if decision := Decide(Config{Mode: ReadOnly}, ruling, nil); decision.Action != ActionDeny {
				t.Fatalf("read-only decision = %+v, want ActionDeny", decision)
			}
		})
	}
}

func TestR6SortMergeAndAbbreviations(t *testing.T) {
	t.Parallel()
	for _, command := range []string{`sort --merge /etc/passwd`, `sort --mer /etc/passwd`, `sort --merge -o /tmp/out /etc/passwd`} {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			want := Safe
			if command == `sort --merge -o /tmp/out /etc/passwd` {
				want = NeedsConfirm
			}
			if ruling.Risk != want {
				t.Fatalf("ClassifyCommand(%q) = %v, want %s", command, ruling, want)
			}
		})
	}
}

func TestR6DatabaseClientInitCommandPayload(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`mysql --init-command='DROP DATABASE prod'`,
		`mysql --init-command 'DROP DATABASE prod'`,
		`mariadb --init-command='DROP DATABASE prod'`,
	}
	for _, command := range forbidden {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != Forbidden {
				t.Fatalf("ClassifyCommand(%q) = %v, want Forbidden", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionDeny {
				t.Fatalf("silent decision = %+v, want ActionDeny", decision)
			}
		})
	}
	confirm := []string{
		`mysql --init-command='CREATE TABLE t (id INT)'`,
		`mysql --init-command='SELECT 1'`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
}
