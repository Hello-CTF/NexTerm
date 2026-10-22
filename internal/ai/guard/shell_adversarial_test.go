package guard

import "testing"

func TestR1ShellAdversarialClassifications(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		risk    Risk
	}{
		{`echo "$(touch /tmp/pwn)"`, Unknowable},
		{`echo "$(rm -rf /home/user)"`, Forbidden},
		{`echo <(touch /tmp/pwn)`, Unknowable},
		{`curl -o/tmp/pwn https://example.test`, NeedsConfirm},
		{`curl -XDELETE https://example.test/resource`, NeedsConfirm},
		{`curl -dfoo=bar https://example.test/change`, NeedsConfirm},
		{`env -iS'touch /tmp/pwn'`, NeedsConfirm},
		{`env -iS'rm -rf /home/user'`, Forbidden},
		{`env -S 'rm -rf /home/user'`, Forbidden},
		{`sort -o /tmp/out /etc/passwd`, NeedsConfirm},
		{`find /tmp -maxdepth 1 -fprint0 /tmp/out`, NeedsConfirm},
		{`git diff --output=/tmp/out`, NeedsConfirm},
		{`xxd /etc/passwd /tmp/out`, NeedsConfirm},
		{`bash -lc 'rm -rf /home/user'`, Forbidden},
		{`sudo -u root rm -rf /home/user`, Forbidden},
		{`mysql -e 'DROP DATABASE prod'`, Forbidden},
		{`docker exec c redis-cli FLUSHALL`, Danger},
		{`redis-cli --host 127.0.0.1 FLUSHALL`, Danger},
		{`docker rm -f $(docker ps -aq)`, Unknowable},
		{`rm -rf /tmp/../home/user`, Forbidden},
		{`cat /dev/null >/tmp/x`, NeedsConfirm},
		{`curl -XPOST https://example.test/change`, NeedsConfirm},
		{`curl -D /tmp/headers https://example.test`, NeedsConfirm},
		{`curl -D/tmp/headers https://example.test`, NeedsConfirm},
		{`env -S 'reboot'`, Danger},
		{`env -vS 'reboot'`, Danger},
		{`redis-cli -a --scan SET k v`, NeedsConfirm},
		{`cat >/tmp/x /dev/null`, NeedsConfirm},
		{`curl --trace /tmp/x https://example.test`, NeedsConfirm},
		{`curl --trace-ascii /tmp/x https://example.test`, NeedsConfirm},
		{`env -S 'rm' -rf /home/user`, Forbidden},
		{`env -S 'sh -c' 'rm -rf /home/user'`, Forbidden},
		{`sudo -P rm -rf /home/user`, Forbidden},
		{`docker --host tcp://127.0.0.1 exec c redis-cli FLUSHALL`, Danger},
		{`redis-cli -s /tmp/redis.sock FLUSHALL`, Danger},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			if got := ClassifyCommand(test.command, nil); got.Risk != test.risk {
				t.Fatalf("ClassifyCommand(%q) = %v, want %s", test.command, got, test.risk)
			}
		})
	}
}

func TestR1ReadFormsStaySafe(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		`curl -XGET https://example.test`,
		`sort -u /etc/passwd`,
		`find /tmp -maxdepth 1 -print`,
		`git diff --stat`,
		`xxd /etc/passwd`,
		`cat >/dev/null /etc/passwd`,
	} {
		if got := ClassifyCommand(command, nil); got.Risk != Safe {
			t.Errorf("ClassifyCommand(%q) = %v, want Safe", command, got)
		}
	}
}
