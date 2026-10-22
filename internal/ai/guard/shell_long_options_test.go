package guard

import "testing"

func TestR5GNULongOptionAbbreviationsFailClosed(t *testing.T) {
	t.Parallel()
	confirm := []string{
		`env --s='touch /tmp/pwn'`,
		`env --sp='touch /tmp/pwn'`,
		`env --split='touch /tmp/pwn'`,
		`env --split-st='touch /tmp/pwn'`,
		`env --split-string='touch /tmp/pwn'`,
		`env --s touch /tmp/pwn`,
		`sort --o /tmp/out /etc/passwd`,
		`sort --ou /tmp/out /etc/passwd`,
		`sort --outp /tmp/out /etc/passwd`,
		`sort --outpu /tmp/out /etc/passwd`,
		`sort --output /tmp/out /etc/passwd`,
		`sort --output=/tmp/out /etc/passwd`,
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

	unknowable := []string{
		`env --unknown-long-option ls`,
		`sort --i /etc/passwd`,
		`sort --unknown-long-option /etc/passwd`,
	}
	for _, command := range unknowable {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != Unknowable {
				t.Fatalf("ClassifyCommand(%q) = %v, want Unknowable", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAsk {
				t.Fatalf("silent decision = %+v, want ActionAsk", decision)
			}
			if decision := Decide(Config{Mode: ReadOnly}, ruling, nil); decision.Action != ActionDeny {
				t.Fatalf("read-only decision = %+v, want ActionDeny", decision)
			}
		})
	}

	for _, command := range []string{`sort --compress-program=/tmp/evil /etc/passwd`} {
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
	forbidden := []string{
		`env --s='rm -rf /home/user'`,
		`env --split='rm -rf /home/user'`,
		`env --split-string='rm -rf /home/user'`,
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
	safe := []string{
		`env ls`,
		`env FOO=bar ls`,
		`env -i FOO=bar ls`,
		`env -u NAME ls`,
		`env --unset NAME ls`,
		`env --u NAME ls`,
		`env -C /tmp ls`,
		`env --chdir /tmp ls`,
		`env -- ls`,
		`sort /etc/passwd`,
		`sort -u /etc/passwd`,
		`sort -k2 -t: /etc/passwd`,
		`sort --key=2 --field-separator=: /etc/passwd`,
		`sort --numeric-sort /etc/passwd`,
		`sort -o- /etc/passwd`,
		`sort --check /etc/passwd`,
		`env --split-s='id'`,
		`env -S 'id'`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			want := Safe
			if command == `env --split-s='id'` || command == `env -S 'id'` {
				want = NeedsConfirm
			}
			if ruling.Risk != want {
				t.Fatalf("ClassifyCommand(%q) = %v, want %s", command, ruling, want)
			}
		})
	}
}

func TestR5CurlWriteOptionsAreNeverSafe(t *testing.T) {
	t.Parallel()
	confirm := []string{
		`curl --etag-save /tmp/etag https://example.test`,
		`curl --etag-save=/tmp/etag https://example.test`,
		`curl -K /tmp/cfg https://example.test`,
		`curl -K/tmp/cfg https://example.test`,
		`curl --config /tmp/cfg https://example.test`,
		`curl --config=/tmp/cfg https://example.test`,
		`curl -J -O https://example.test`,
		`curl --remote-header-name https://example.test`,
		`curl --stderr /tmp/log https://example.test`,
		`curl --stderr=/tmp/log https://example.test`,
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
	for _, command := range []string{`curl -sXGET https://example.test`, `curl --stderr - https://example.test`, `curl --etag-compare /tmp/etag https://example.test`} {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

func TestR5DockerBulkDeletionWithInnerGlobalOptions(t *testing.T) {
	t.Parallel()
	ask := []string{
		`docker rm -f $(docker -H tcp://127.0.0.1:2375 ps -aq)`,
		`docker rm -f $(docker -H tcp://127.0.0.1:2375 ps -q)`,
		`docker rm -f $(docker --host tcp://127.0.0.1:2375 ps -aq)`,
		`docker rm -f $(docker --host=tcp://127.0.0.1:2375 ps -aq)`,
		`docker rmi -f $(docker --config /tmp/cfg images -q)`,
		`docker rm -f $(sudo docker -H tcp://127.0.0.1:2375 ps -aq)`,
		`docker container rm -f $(docker -H tcp://127.0.0.1:2375 ps -aq)`,
		`docker image rm -f $(docker -H tcp://127.0.0.1:2375 images -q)`,
		`podman rm -f $(podman -H tcp://127.0.0.1:2375 ps -aq)`,
		`docker rm -f $(docker -H tcp://127.0.0.1:2375 ps)`,
	}
	for _, command := range ask {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != Unknowable {
				t.Fatalf("ClassifyCommand(%q) = %v, want Unknowable", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAsk {
				t.Fatalf("silent decision = %+v, want ActionAsk", decision)
			}
		})
	}
}

func TestR5RedisRepeatOptionsExposeRealCommand(t *testing.T) {
	t.Parallel()
	danger := []string{
		`redis-cli -r 1 FLUSHALL`,
		`redis-cli -i 1 FLUSHALL`,
		`redis-cli -r 1 -i 1 FLUSHALL`,
		`redis-cli -r 1 -h localhost FLUSHALL`,
		`redis-cli -r 1 FLUSHDB`,
		`redis-cli -r 1 SHUTDOWN`,
		`valkey-cli -r 1 FLUSHALL`,
		`redis-cli -t 5 -r 1 FLUSHALL`,
		`redis-cli --rdb /tmp/dump.rdb -r 1 FLUSHALL`,
		`redis-cli -D INFO FLUSHALL`,
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
	safe := []string{
		`redis-cli -r 1 INFO`,
		`redis-cli -r 1 PING`,
		`redis-cli -i 1 GET key`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
	for _, command := range []string{`redis-cli -r 1 SET k v`, `redis-cli --rdb /tmp/dump.rdb`, `redis-cli -r 1 DEBUG JMAP`} {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
}
