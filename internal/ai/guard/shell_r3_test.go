package guard

import "testing"

func TestR3ShellAdversarialClassifications(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		risk    Risk
	}{
		{`curl -sO https://example.test/pwn`, NeedsConfirm},
		{`curl --remote-name https://example.test/pwn`, NeedsConfirm},
		{`curl -c /tmp/cookies https://example.test`, NeedsConfirm},
		{`sort --out=/tmp/out /etc/passwd`, NeedsConfirm},
		{`env --split-s='touch /tmp/pwn'`, NeedsConfirm},
		{`env -S'rm\_-rf\_/home/echo'`, Forbidden},
		{`sudo -D /tmp rm -rf /home/user`, Forbidden},
		{`docker exec c sudo -u root rm -rf /home/user`, Forbidden},
		{`docker container rm -f $(docker ps -q -a)`, Unknowable},
		{`docker container rm -f $(docker ps -aq)`, Unknowable},
		{`docker rmi -f $(docker images -q)`, Unknowable},
		{`echo =(touch /tmp/pwn)`, Unknowable},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			if got := ClassifyCommand(test.command, nil); got.Risk != test.risk {
				t.Fatalf("ClassifyCommand(%q) = %v, want %s", test.command, got, test.risk)
			}
		})
	}
}

func TestR3QuotedAllowlistDoesNotSanitizeUserFunction(t *testing.T) {
	t.Parallel()
	if got := ClassifySQL(`SELECT "COUNT"();`, nil); got.Risk != NeedsConfirm {
		t.Fatalf("quoted user function = %v, want NeedsConfirm", got)
	}
}
