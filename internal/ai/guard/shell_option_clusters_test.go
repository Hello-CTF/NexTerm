package guard

import "testing"

func TestR4OptionClusterAndBulkDeletionClassifications(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		risk    Risk
	}{
		{`xxd -a /etc/passwd /tmp/out`, NeedsConfirm},
		{`curl -sD/tmp/headers https://example.test`, NeedsConfirm},
		{`curl -sXPOST https://example.test/change`, NeedsConfirm},
		{`sort -uo/tmp/out /etc/passwd`, NeedsConfirm},
		{`redis-cli -D INFO FLUSHALL`, Danger},
		{`redis-cli -d INFO FLUSHALL`, Danger},
		{`sudo -Eu root rm -rf /home/user`, Forbidden},
		{`psql -qc 'DROP DATABASE prod'`, Forbidden},
		{`docker rm -f $(docker ps -q -a)`, Unknowable},
		{`docker rm -f $(docker ps -a -q)`, Unknowable},
		{`docker rmi -f $(docker images -q)`, Unknowable},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			if got := ClassifyCommand(test.command, nil); got.Risk != test.risk {
				t.Fatalf("ClassifyCommand(%q) = %v, want %s", test.command, got, test.risk)
			}
		})
	}
}

func TestR4ReadFormsRemainSafe(t *testing.T) {
	t.Parallel()
	for _, command := range []string{`xxd -a /etc/passwd`, `curl -sXGET https://example.test`, `sort -u /etc/passwd`} {
		if got := ClassifyCommand(command, nil); got.Risk != Safe {
			t.Errorf("ClassifyCommand(%q) = %v, want Safe", command, got)
		}
	}
}
