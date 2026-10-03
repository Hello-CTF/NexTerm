package guard

import (
	"fmt"
	"testing"
)

// R3 P1-1: wrapper/kubectl recursion preserves argv token boundaries and
// xargs -I replacement is modeled. Silent auto-allows NeedsConfirm, so every
// nested-execution bypass must rule at least Danger.
func TestR7WrapperArgvPreserved(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`nice sh -c 'rm -rf /home/user'`,
		`nohup sh -c 'rm -rf /home/user'`,
		`timeout 5 sh -c 'rm -rf /home/user'`,
		`timeout -k 5 10 sh -c 'rm -rf /home/user'`,
		`stdbuf -o0 sh -c 'rm -rf /home/user'`,
		`command sh -c 'rm -rf /home/user'`,
		`time sh -c 'rm -rf /home/user'`,
		`watch -n1 sh -c 'rm -rf /home/user'`,
		`setsid sh -c 'rm -rf /home/user'`,
		`systemd-run sh -c 'rm -rf /home/user'`,
		`systemd-run --unit=x sh -c 'rm -rf /home/user'`,
		`nice timeout 5 sh -c 'rm -rf /home/user'`,
		`kubectl exec pod -- sh -c 'rm -rf /home/user'`,
		`kubectl exec -c container -n ns pod -- sh -c 'rm -rf /home/user'`,
		`echo 'rm -rf /home/user' | xargs -I{} sh -c {}`,
		`echo 'rm -rf /home/user' | xargs -I {} sh -c {}`,
		`echo 'rm -rf /home/user' | xargs --replace={} sh -c {}`,
		`echo 'rm -rf /home/user' | xargs -I{} sh -c '{}' extra`,
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
	// Uncontrolled xargs input must not fall to NeedsConfirm either.
	forbidden = append(forbidden,
		`echo 'rm -rf /home/user' | xargs -I{} sh -c {} | cat`,
	)
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
	danger := []string{
		`xargs -I{} sh -c {}`,
		`xargs -a /tmp/items rm`,
		`echo x | xargs -a /tmp/items rm`,
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
	// Benign nested commands keep their ruling through the argv path.
	safe := []string{
		`nice sh -c 'id'`,
		`timeout 5 sh -c 'id'`,
		`echo 'id' | xargs -I{} sh -c {}`,
		`echo 'id' | xargs -I{} echo {}`,
		`echo 'a b' | xargs printf '%s'`,
		`kubectl exec pod -- ls`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk == Danger || ruling.Risk == Forbidden {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe or NeedsConfirm", command, ruling)
			}
		})
	}
}

// R3 P1-2: echo/printf producers emit the real payload; at -f takes
// precedence over stdin; at -l stays read-only.
func TestR7StdinProducerModeling(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`echo -e 'rm -rf /home/user' | at now`,
		`echo -n 'rm -rf /home/user' | at now`,
		`echo -E 'rm -rf /home/user' | at now`,
		`printf '%s' 'rm -rf /home/user' | at now`,
		`printf 'rm -rf /home/user' | at now`,
		`printf '%s\n' 'rm -rf /home/user' | at now`,
		`printf '%s' /home/user | xargs rm -rf`,
		`printf '%s\n' /home/user | xargs rm -rf`,
		`echo 'rm -rf /home/user' | batch`,
		`echo 'rm -rf /home/user' | nice at now`,
		`echo 'rm -rf /home/user' | at -q a now`,
		`echo 'rm -rf /home/user' | at -t 202610031200`,
		`echo 'rm -rf /home/user' | sh`,
		`printf 'rm -rf /home/user' | sh`,
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
	danger := []string{
		`echo benign | at -f /tmp/payload now`,
		`echo benign | at -f/tmp/payload now`,
		`echo benign | at --file=/tmp/payload now`,
		`echo benign | at --file /tmp/payload now`,
		`at -f /tmp/payload now`,
		`printf '%q' 'rm -rf /home/user' | at now`,
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
		`at -l`,
		`at -l -q a`,
		`echo x | at -l`,
		`echo -e 'id' | xargs -I{} echo {}`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
	// at/batch install scheduled jobs, so even provably read-only payloads
	// confirm at minimum (R5R4 contract).
	confirm := []string{
		`at -r 5`,
		`at -d 5`,
		`echo benign | batch -m`,
		`echo -e 'ls' | at now`,
		`printf '%s' 'ls' | at now`,
		`echo 'id' | at now`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
}

// R3 P1-3: su/sudo attached and clustered option forms reach the recursive
// and edit checks.
func TestR7SuSudoAttachedAndClustered(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`su -c'rm -rf /home/user'`,
		`su -lc 'rm -rf /home/user'`,
		`su root -c'rm -rf /home/user'`,
		`su -s /bin/sh -c'rm -rf /home/user'`,
		`su --command 'rm -rf /home/user'`,
		`su --session-command 'rm -rf /home/user'`,
		`su --session-command='rm -rf /home/user'`,
		`sudo -eu root /etc/passwd`,
		`sudo -eu root /etc/shadow`,
		`sudo -eu root /root/.ssh/authorized_keys`,
		`sudo -u root -e /etc/sudoers`,
		`sudo --edit /etc/cron.d/x`,
		`sudo --edi /etc/passwd`,
		`sudoedit /etc/cron.d/x`,
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
	danger := []string{
		`sudo -eu root /tmp/notes.txt`,
		`sudo -e/tmp/notes.txt`,
		`sudo -e /tmp/notes.txt`,
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
	// Established benign and recursive forms must not regress.
	confirm := []string{
		`su -c 'id'`,
		`su --session-command 'id'`,
		`sudo -u root id`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
	forbiddenRecursive := []string{
		`sudo -u root rm -rf /home/user`,
		`sudo -Eu root rm -rf /home/user`,
		`sudo -D /tmp rm -rf /home/user`,
		`sudo -P rm -rf /home/user`,
		`doas -a su rm -rf /home/user`,
	}
	for _, command := range forbiddenRecursive {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Forbidden {
				t.Fatalf("ClassifyCommand(%q) = %v, want Forbidden", command, ruling)
			}
		})
	}
}

// R3 P1-4: non-shell interpreter inline execution covers attached short
// options and long --eval=/--command= forms; shells keep recursion.
func TestR7InterpreterInlineForms(t *testing.T) {
	t.Parallel()
	danger := []string{
		`python3 -c'import shutil; shutil.rmtree("/home/user")'`,
		`python3 -c'print(1)'`,
		`python3 -c 'print(1)'`,
		`perl -e'system("rm -rf /home/user")'`,
		`perl -e 'system("rm -rf /home/user")'`,
		`perl -E'say 1'`,
		`osascript -e'do shell script "rm -rf /home/user"'`,
		`node --eval='require("fs").rmSync("/home/user",{recursive:true})'`,
		`node --eval 'console.log(1)'`,
		`node -e'console.log(1)'`,
		`ruby -e'puts 1'`,
		`php -r'echo 1;'`,
		`lua -e'print(1)'`,
		`node -p'1+1'`,
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
	forbidden := []string{
		`bash -c'rm -rf /home/user'`,
		`sh -c'rm -rf /home/user'`,
		`zsh -c 'rm -rf /home/user'`,
		`dash -c'rm -rf /home/user'`,
	}
	for _, command := range forbidden {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != Forbidden {
				t.Fatalf("ClassifyCommand(%q) = %v, want Forbidden", command, ruling)
			}
		})
	}
	confirm := []string{
		`python3 -m http.server`,
		`node --inspect index.js`,
		`python3 script.py`,
		`bash -x script.sh`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
}

// R3 P1-5: force deletion fed by ID-producing enumerations is bulk
// destruction; plain table listings stay NeedsConfirm.
func TestR7DockerFormattedEnumeration(t *testing.T) {
	t.Parallel()
	danger := []string{
		`docker rm -f $(docker ps --format '{{.ID}}')`,
		`docker rm -f $(docker ps --format='{{.ID}}')`,
		`docker rm -f $(docker ps -q --format '{{.ID}}')`,
		`docker rmi -f $(docker images --format '{{.ID}}')`,
		`docker rmi -f $(docker image ls --format '{{.ID}}')`,
		`docker rm -f $(docker container ps --format '{{.ID}}')`,
		`docker rm -fv $(docker ps --format '{{.Names}}')`,
		`podman rm -f $(podman ps --format '{{.ID}}')`,
		`docker rm -f $(sudo docker ps --format '{{.ID}}')`,
		`docker rm -f $(docker -H tcp://127.0.0.1:2375 ps --format '{{.ID}}')`,
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
	confirm := []string{
		`docker rm -f $(docker ps)`,
		`docker rm -f $(docker ps -a)`,
		`docker rm $(docker ps -q)`,
		`docker rm -f web`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
}

// R3 P1-6: ssh -o execution keywords and network-tool exec forms are local
// code execution and must not stay NeedsConfirm.
func TestR7SSHAndNetworkExecutionOptions(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`ssh -o ProxyCommand='rm -rf /home/user' host`,
		`ssh -oProxyCommand='rm -rf /home/user' host`,
		`ssh -o proxycommand='rm -rf /home/user' host`,
		`ssh -o LocalCommand='rm -rf /home/user' host`,
		`ssh -o RemoteCommand='rm -rf /home/user' host`,
		`ssh -o ProxyCommand='rm -rf /home/user' -p 2222 user@host`,
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
	danger := []string{
		`ssh -o ProxyCommand='id' host`,
		`ssh -o ProxyCommand='sh -c id' host`,
		`ncat --exec /bin/sh 127.0.0.1 4444`,
		`ncat --exec=/bin/sh 127.0.0.1 4444`,
		`ncat --sh-exec /bin/sh 127.0.0.1 4444`,
		`socat EXEC:/bin/sh tcp:127.0.0.1:4444`,
		`socat EXEC:/bin/sh,pty tcp:127.0.0.1:4444`,
		`socat SYSTEM:'id' tcp:127.0.0.1:4444`,
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
	confirm := []string{
		`ssh -o StrictHostKeyChecking=no host`,
		`ssh -o ProxyCommand=none host`,
		`ssh host`,
		`nc -l 127.0.0.1 4444`,
		`socat tcp-listen:4444 tcp:127.0.0.1:5555`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
}

// R3 P1-7: global options before systemctl/git subcommands and database
// client escape forms are classified by their real effect.
func TestR7GlobalOptionsAndClientEscapes(t *testing.T) {
	t.Parallel()
	danger := []string{
		`systemctl --no-block stop ssh`,
		`systemctl --user stop ssh`,
		`systemctl --user --no-block mask ssh`,
		`systemctl -H host stop ssh`,
		`systemctl --host host isolate multi-user.target`,
		`git -C repo clean -fdx`,
		`git -Crepo clean -fdx`,
		`git -c core.pager='id' status`,
		`git -c core.pager id log`,
		`git -ccore.pager='id' log`,
		// R5R4: editor values are executable configs regardless of subcommand.
		`git -c core.editor=vi status`,
		`git -c sequence.editor=vi status`,
		`mysql -e 'source /tmp/evil.sql'`,
		`mysql -e 'SOURCE /tmp/evil.sql'`,
		`mysql -e '\. /tmp/evil.sql'`,
		`mysql --init-command='source /tmp/evil.sql'`,
		`psql -c '\i /tmp/evil.sql'`,
		`psql -c '\ir /tmp/evil.sql'`,
		`psql -c '\! id'`,
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
	forbidden := []string{
		`psql -c '\! rm -rf /home/user'`,
		`git -c core.pager='rm -rf /home/user' status`,
		`git -c core.sshCommand='rm -rf /home/user' fetch`,
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
		`systemctl --failed --no-pager`,
		`systemctl --user status ssh`,
		`systemctl --no-block list-units`,
		`git -C repo status --short`,
		`git clean -n`,
		`git clean --dry-run`,
		`git clean -ndx`,
		`git -C repo clean -n`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
	confirm := []string{
		`mysql -e 'select 1'`,
		`psql -c 'SELECT 1'`,
		`psql -c '\d'`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
	// Plain clean without -f/-n refuses to run (clean.requireForce); the
	// pinned conservative ruling stays Danger.
	for _, command := range []string{`git clean`, `git -C repo clean`} {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Danger {
				t.Fatalf("ClassifyCommand(%q) = %v, want Danger", command, ruling)
			}
		})
	}
}

// R3 P1-8: critical-path protection is canonical and covers exact roots and
// file-producing commands, not only redirection and sudo-edit.
func TestR7CanonicalCriticalTargets(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`rm -rf /Users`,
		`rm -rf /Users/`,
		`rm -rf /tmp/../Users`,
		`rm -rf /tmp/../home/user`,
		`echo pwn > /tmp/../etc/cron.d/x`,
		`echo pwn > //etc/cron.d/x`,
		`echo pwn > /etc/../etc/cron.d/x`,
		`echo pwn >> /tmp/../root/.ssh/authorized_keys`,
		`dd if=/dev/zero of=/dev//disk0`,
		`dd if=/dev/zero of=/dev/./disk0`,
		`dd if=/dev/zero of=/tmp/../dev/disk0`,
		`cp /tmp/x /etc/cron.d/x`,
		`cp /tmp/x /tmp/../etc/cron.d/x`,
		`mv /tmp/x /etc/sudoers.d/y`,
		`install /tmp/x /etc/cron.d/x`,
		`ln -s /tmp/x /etc/cron.d/x`,
		`echo pwn | tee /etc/cron.d/x`,
		`truncate -s 0 /etc/cron.d/x`,
		`shred /etc/cron.d/x`,
		`rm /etc/cron.d/x`,
		`rm -rf /etc/cron.d`,
		`unlink /etc/cron.d/x`,
		`cp -t /etc/cron.d /tmp/x`,
		`cp --target-directory=/etc/cron.d /tmp/x`,
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
		`cp /tmp/x /tmp/y`,
		`mv /tmp/x /tmp/y`,
		`rm /tmp/x`,
		`rm -rf /tmp/x`,
		`dd if=/dev/zero of=/tmp/x`,
		`echo pwn | tee /tmp/x`,
		`rm -rf /Users2`,
		`truncate -s 0 /tmp/x`,
		`ln -s /tmp/x /tmp/y`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
}

// R3 P2-1: E-section write options and su --session-command regressions.
func TestR7CurlCacheWritesAndSuSessionCommand(t *testing.T) {
	t.Parallel()
	confirm := []string{
		`curl --libcurl /tmp/code.c https://example.test`,
		`curl --libcurl=/tmp/code.c https://example.test`,
		`curl --alt-svc /tmp/svc https://example.test`,
		`curl --alt-svc=/tmp/svc https://example.test`,
		`curl --hsts /tmp/hsts https://example.test`,
		`curl --hsts=/tmp/hsts https://example.test`,
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
		`curl -sG -d foo=bar https://example.test`,
		`curl -sG --libcurl - https://example.test`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
	if ruling := ClassifyCommand(`su --session-command 'rm -rf /home/user'`, nil); ruling.Risk != Forbidden {
		t.Fatalf("su --session-command = %v, want Forbidden", ruling)
	}
	if ruling := ClassifyCommand(`su --session-command 'id'`, nil); ruling.Risk != NeedsConfirm {
		t.Fatalf("su --session-command id = %v, want NeedsConfirm", ruling)
	}
}

// TestR7VariantCorpusSweep generates attached/separate/clustered/case/alias
// variants of every R3 finding and asserts the conservative floor: nothing
// that executes, deletes or writes critically may fall to NeedsConfirm.
func TestR7VariantCorpusSweep(t *testing.T) {
	t.Parallel()
	payloads := []string{`rm -rf /home/user`, `id`}
	wrapVariants := []struct{ prefix, suffix string }{
		{`nice `, ``},
		{`nohup `, ``},
		{`timeout 5 `, ``},
		{`timeout -k 5 10 `, ``},
		{`stdbuf -o0 `, ``},
		{`command `, ``},
		{`time `, ``},
		{`watch -n1 `, ``},
		{`setsid `, ``},
		{`systemd-run `, ``},
		{`systemd-run --unit=u --description=d `, ``},
	}
	interpreterVariants := []string{
		`sh -c '%s'`,
		`sh -c'%s'`,
		`bash -lc '%s'`,
		`bash -c'%s'`,
	}
	for _, payload := range payloads {
		for _, wrap := range wrapVariants {
			for _, form := range interpreterVariants {
				command := wrap.prefix + sprintfVariant(form, payload) + wrap.suffix
				ruling := ClassifyCommand(command, nil)
				want := Forbidden
				if payload == `id` {
					want = NeedsConfirm
				}
				if ruling.Risk != want {
					t.Errorf("ClassifyCommand(%q) = %v, want %s", command, ruling, want)
				}
			}
		}
	}
	// xargs replacement variants.
	for _, command := range []string{
		`echo 'rm -rf /home/user' | xargs -I{} sh -c {}`,
		`echo 'rm -rf /home/user' | xargs -I {} sh -c {}`,
		`echo 'rm -rf /home/user' | xargs --replace={} sh -c {}`,
		`echo 'rm -rf /home/user' | xargs --replace {} sh -c {}`,
		`echo 'rm -rf /home/user' | xargs -I{} sh -c '{}' _`,
		`echo 'rm -rf /home/user' | xargs -I{} bash -c {}`,
		`printf '%s' 'rm -rf /home/user' | xargs -I{} sh -c {}`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != Forbidden {
			t.Errorf("ClassifyCommand(%q) = %v, want Forbidden", command, ruling)
		}
	}
	// su/sudo attached, separated, clustered and case variants.
	for _, command := range []string{
		`su -c'rm -rf /home/user'`,
		`su -c 'rm -rf /home/user'`,
		`su root -c'rm -rf /home/user'`,
		`su root -c 'rm -rf /home/user'`,
		`su --command='rm -rf /home/user'`,
		`su --command 'rm -rf /home/user'`,
		`su --session-command='rm -rf /home/user'`,
		`su --session-command 'rm -rf /home/user'`,
		`sudo -u root rm -rf /home/user`,
		`sudo -uroot rm -rf /home/user`,
		`sudo -Eu root rm -rf /home/user`,
		`sudo -D /tmp rm -rf /home/user`,
		`sudo -e /etc/passwd`,
		`sudo -eu root /etc/passwd`,
		`sudo --edit /etc/passwd`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != Forbidden {
			t.Errorf("ClassifyCommand(%q) = %v, want Forbidden", command, ruling)
		}
	}
	// Interpreter inline variants.
	for _, command := range []string{
		`python3 -c'print(1)'`,
		`python3 -c 'print(1)'`,
		`python3 --command 'print(1)'`,
		`perl -e'print 1'`,
		`perl -e 'print 1'`,
		`ruby -e'puts 1'`,
		`node --eval='1'`,
		`node --eval '1'`,
		`node -e'1'`,
		`php -r'echo 1;'`,
		`lua -e'print(1)'`,
		`osascript -e'print(1)'`,
		`pwsh -Command '1'`,
		`pwsh -command '1'`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != Danger {
			t.Errorf("ClassifyCommand(%q) = %v, want Danger", command, ruling)
		}
	}
	// Docker enumeration variants.
	for _, command := range []string{
		`docker rm -f $(docker ps -q)`,
		`docker rm -f $(docker ps --quiet)`,
		`docker rm -f $(docker ps --quiet=true)`,
		`docker rm -f $(docker ps -aq)`,
		`docker rm -f $(docker ps -qa)`,
		`docker rm -fv $(docker ps -q)`,
		`docker rm -vf $(docker ps -q)`,
		`docker rm --force $(docker ps -q)`,
		`docker rm -f $(docker ps --format '{{.ID}}')`,
		`docker rm -f $(docker ps --format='{{.ID}}')`,
		`docker rm -f $(docker container ps -q)`,
		`docker rm -f $(docker container ls -q)`,
		`docker rm -f $(docker container list -q)`,
		`docker rmi -f $(docker images -q)`,
		`docker rmi -f $(docker image ls -q)`,
		`docker rmi -f $(docker image list -q)`,
		`docker rm -f $(podman ps -q)`,
		`docker rm -f $(sudo docker ps -q)`,
		`docker rm -f $(doas docker ps -q)`,
		`docker rm -f $(docker -H tcp://127.0.0.1:2375 ps -q)`,
		`docker rm -f $(docker --host=tcp://127.0.0.1:2375 ps -q)`,
		`docker rm -f $(docker --config /tmp/cfg ps -q)`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != Danger {
			t.Errorf("ClassifyCommand(%q) = %v, want Danger", command, ruling)
		}
	}
	// ssh/network execution variants.
	for _, command := range []string{
		`ssh -o ProxyCommand='id' h`,
		`ssh -oProxyCommand='id' h`,
		`ssh -o proxycommand='id' h`,
		`ssh -o PROXYCOMMAND='id' h`,
		`ssh -o LocalCommand='id' h`,
		`ssh -o RemoteCommand='id' h`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != Danger {
			t.Errorf("ClassifyCommand(%q) = %v, want Danger", command, ruling)
		}
	}
	// Global-option and escape variants.
	for _, command := range []string{
		`systemctl stop ssh`,
		`systemctl --no-block stop ssh`,
		`systemctl --user stop ssh`,
		`systemctl --user --no-block stop ssh`,
		`systemctl -H h stop ssh`,
		`systemctl --host=h stop ssh`,
		`git clean -fdx`,
		`git -C r clean -fdx`,
		`git -Cr clean -fdx`,
		`mysql -e 'source /tmp/x.sql'`,
		`mysql -e 'SOURCE /tmp/x.sql'`,
		`mysql -e 'source /tmp/x.sql' db`,
		`psql -c '\! id'`,
		`psql -c '\i /tmp/x.sql'`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != Danger {
			t.Errorf("ClassifyCommand(%q) = %v, want Danger", command, ruling)
		}
	}
	// Canonical target aliases.
	for _, command := range []string{
		`rm -rf /Users`,
		`rm -rf /Users/`,
		`rm -rf /tmp/../Users`,
		`rm -rf /./Users`,
		`echo x > /tmp/../etc/cron.d/x`,
		`echo x > //etc/cron.d/x`,
		`echo x > /etc/./cron.d/x`,
		`dd if=/dev/zero of=/dev//disk0`,
		`dd if=/dev/zero of=/dev/./disk0`,
		`dd if=/dev/zero of=/tmp/../dev/disk0`,
		`cp /tmp/x /tmp/../etc/cron.d/x`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != Forbidden {
			t.Errorf("ClassifyCommand(%q) = %v, want Forbidden", command, ruling)
		}
	}
	// Conservative floors for unparseable wrapper/nested forms.
	for _, command := range []string{
		`nice -x rm -rf /home/user`,
		`nice --unknown rm -rf /home/user`,
		`timeout --unknown-option 5 rm -rf /home/user`,
		`xargs -Z rm`,
		`xargs --unknown-option rm`,
		`at -Z now`,
		`echo x | batch -Z`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != Danger {
			t.Errorf("ClassifyCommand(%q) = %v, want Danger", command, ruling)
		}
	}
	// Read-only forms must keep their narrow positive allow.
	for _, command := range []string{
		`nice ls`,
		`timeout 5 ls`,
		`echo x | xargs ls`,
		`echo x | xargs -I{} ls {}`,
		`at -l`,
		`git clean -n`,
		`systemctl --failed`,
		`systemctl --user status ssh`,
		`git -C r status`,
		`printf '%s' x | xargs echo`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
			t.Errorf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
		}
	}
	// Established NeedsConfirm boundaries must not drift.
	for _, command := range []string{
		`docker rm -f $(docker ps)`,
		`ssh -o ProxyCommand=none h`,
		`git push origin main`,
		`git push --force origin main`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
			t.Errorf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
		}
	}
	// Read-only HTTP GET stays Safe by design.
	for _, command := range []string{
		`curl https://example.test`,
		`curl -sG -d foo=bar https://example.test`,
	} {
		if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
			t.Errorf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
		}
	}
}

func sprintfVariant(format, payload string) string {
	return fmt.Sprintf(format, payload)
}
