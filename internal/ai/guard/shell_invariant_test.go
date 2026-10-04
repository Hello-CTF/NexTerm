package guard

import (
	"fmt"
	"strings"
	"testing"
)

func TestSilentOnlyAutoAllowsT0T1(t *testing.T) {
	t.Parallel()
	commands := []string{

		`sudo rm -rf /home/user`,
		`doas rm -rf /home/user`,
		`sudo -u root rm -rf /home/user`,
		`sudo -uroot rm -rf /home/user`,
		`sudo -Eu root rm -rf /home/user`,
		`sudo -D /tmp rm -rf /home/user`,
		`env rm -rf /home/user`,
		`env -uFOO rm -rf /home/user`,
		`env -iu FOO rm -rf /home/user`,
		`env --argv0=rm /bin/rm -rf /home/user`,
		`nice rm -rf /home/user`,
		`nohup rm -rf /home/user`,
		`timeout 5 rm -rf /home/user`,
		`timeout -k 5 10 rm -rf /home/user`,
		`stdbuf -o0 rm -rf /home/user`,
		`command rm -rf /home/user`,
		`time rm -rf /home/user`,
		`watch -n1 rm -rf /home/user`,
		`setsid rm -rf /home/user`,
		`systemd-run rm -rf /home/user`,
		`systemd-run --unit=x rm -rf /home/user`,
		`echo 'rm -rf /home/user' | at now`,
		`echo 'rm -rf /home/user' | batch`,
		`echo 'rm -rf /home/user' | nice at now`,
		`printf '%s' 'rm -rf /home/user' | at now`,
		`echo 'rm -rf /home/user' | xargs -I{} sh -c {}`,
		`echo '/home/user' | xargs rm -rf`,

		`bash -c'rm -rf /home/user'`,
		`bash -ce 'rm -rf /home/user'`,
		`bash -xce 'rm -rf /home/user'`,
		`sh -c 'rm -rf /home/user'`,
		`SUDO -u root rm -rf /home/user`,
		`su -c'rm -rf /home/user'`,
		`su root -c'rm -rf /home/user'`,
		`su --session-command='rm -rf /home/user'`,
		`git -C repo clean -fdx`,
		`git --git-dir=/tmp/repo clean -fdx`,
		`git -c alias.pwn='!rm -rf /home/user' pwn`,
		`systemctl --user stop ssh`,
		`systemctl -H host stop ssh`,
		`kubectl exec --context prod pod -- rm -rf /home/user`,
		`docker exec c rm -rf /home/user`,
		`docker exec c sh -c 'rm -rf /home/user'`,
		`podman exec c rm -rf /home/user`,
		`ssh user@host 'rm -rf /home/user'`,
		`ssh -o ProxyCommand='rm -rf /home/user' host`,

		`"rm" -rf /home/user`,
		`rm '-rf' /home/user`,
		`rm -rf "/home/user"`,
		`rm -rf '/home/user'`,

		`rm -rf $HOME/sub`,
		`rm -rf ${HOME}/sub`,
		`rm -rf $d`,
		`echo $(rm -rf /home/user)`,
		`echo ` + "`rm -rf /home/user`",
		`echo "$(rm -rf /home/user)"`,
		`echo <(rm -rf /home/user)`,
		`echo >(rm -rf /home/user)`,
		`rm -rf /home/*`,
		`rm -rf /home/user*`,
		`echo pwn > /etc/cron.d/$f`,
		`echo pwn > /etc/cron.d/*`,

		"sh <<'EOF'\nrm -rf /home/user\nEOF",
		"bash <<'EOF'\nrm -rf /home/user\nEOF",
		"at now <<'EOF'\nrm -rf /home/user\nEOF",
		"sh <<EOF\nrm -rf $x\nEOF",
		"mysql <<'EOF'\nDROP DATABASE prod\nEOF",

		`python3 -c 'import shutil; shutil.rmtree("/home/user")'`,
		`perl -e 'system("rm -rf /home/user")'`,
		`bash`,
		`sudo bash`,
		`su`,
		`alias r='rm -rf /home/user'`,

		`bash -i > /dev/tcp/127.0.0.1/4444 2>&1`,
		`socat EXEC:/bin/sh tcp:127.0.0.1:4444`,
		`ncat -e/bin/sh 127.0.0.1 4444`,
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action == ActionAllow {
				t.Fatalf("ClassifyCommand(%q) = %v, silent decision = %+v, must not allow", command, ruling, decision)
			}
		})
	}
}

func TestSilentAllowsProvableCommands(t *testing.T) {
	t.Parallel()
	commands := []string{
		`ls -la /tmp`,
		`cat /etc/os-release`,
		`grep -r pattern /var/log`,
		`git status --short`,
		`git push origin main`,
		`docker ps -a`,
		`kubectl get pods -A`,
		`systemctl status ssh`,
		`curl https://example.test`,
		`curl -sG -d foo=bar https://example.test`,
		`wget -qO- https://example.test`,
		`touch /tmp/x`,
		`mkdir -p /tmp/x/y`,
		`cp /tmp/x /tmp/y`,
		`mv /tmp/x /tmp/y`,
		`rm /tmp/x`,
		`echo pwn > /tmp/x`,
		`echo pwn | tee /tmp/x`,
		`tar -tf archive.tar`,
		`env ls`,
		`env FOO=bar ls`,
		`nice ls`,
		`timeout 5 ls`,
		`echo x | xargs`,
		`echo x | xargs ls`,
		`echo x | grep x`,
		`echo x | xargs rm`,
		`rm *.log`,
		`[ -f /tmp/x ]`,
		`echo 'a b' | xargs printf '%s'`,
		`at -l`,
		`crontab -l`,
		`git clean -n`,
		`ssh host`,
		`nc -l 127.0.0.1 4444`,
		`psql -c 'SELECT 1'`,
		`mysql -e 'select 1'`,
		`redis-cli PING`,
		`redis-cli --scan`,
		`export PATH=/usr/bin`,
		`SELECT * FROM users`,
		`sort -u /etc/passwd`,
		`find /tmp -maxdepth 2 -name '*.log'`,
		`docker exec c ls`,
		`kubectl exec pod -- ls`,
		`su -c 'id'`,
		`sudo -u root id`,
		`bash -c 'id'`,
		`echo 'id' | sh`,
		`cat <<'EOF'
hello world
EOF`,
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk > NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe or NeedsConfirm", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAllow {
				t.Fatalf("silent decision = %+v, want ActionAllow", decision)
			}
		})
	}
}

func TestDynamicConstructsAreUnknowable(t *testing.T) {
	t.Parallel()
	unknowable := []string{
		`echo $x`,
		`echo ${x}`,
		`echo ${x:-default}`,
		`echo ${!indirect}`,
		`echo $((6*7))`,
		`echo $[6*7]`,
		`echo $(id)`,
		`echo ` + "`id`",
		`echo <(id)`,
		`echo >(id)`,
		`echo @(foo|bar)`,
		`echo $'a\nb'`,
		`cat $file`,
		`ls $dir`,
		`rm -rf $HOME`,
		`echo x > $f`,
		`echo x > ${f}`,
		`echo x > "$(f)"`,
		`echo x > *.log`,
		`echo x > /tmp/*.log`,
		`export PATH=$HOME/bin`,
		`FOO=$x ls`,
		`FOO=$(id) ls`,
		`cat <<EOF
$x
EOF`,
		`cat <<< $x`,
		`echo one > /tmp/$(f)`,
		`for f in $files; do echo x; done`,
		`case $x in a) ls;; esac`,
		`f() { ls; }`,
		`((x++))`,
		`let x=1+2`,
		`echo a | $consumer`,
		`$cmd arg`,
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
		})
	}
	safe := []string{
		`echo hello`,
		`echo "hello world"`,
		`echo 'hello world'`,
		`echo *.log`,
		`cat /tmp/*.log`,
		`find /tmp -name '*.log'`,
		`echo a{b,c}d`,
		`echo one; echo two`,
		`ls | grep x`,
		`[[ -f /tmp/x ]]`,
		`(ls; pwd)`,
		`{ ls; pwd; }`,
		`! ls`,
		`time ls`,
		`for f in a b; do echo x; done`,
		`case x in a) ls;; esac`,
		`export PATH=/usr/bin`,
		`FOO=bar ls`,
		`cat <<'EOF'
hello $x
EOF`,
		`cat <<< 'hello'`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

func TestStdinExecutionConsumers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		risk    Risk
	}{

		{`echo 'rm -rf /home/user' | sh`, Forbidden},
		{`echo 'id' | sh`, NeedsConfirm},
		{`echo 'rm -rf /home/user' | at now`, Forbidden},
		{`echo 'id' | at now`, NeedsConfirm},
		{`echo '/home/user' | xargs rm -rf`, Forbidden},
		{`echo x | xargs`, Safe},
		{`echo x | xargs rm`, NeedsConfirm},
		{`echo 'rm -rf /home/user' | xargs -I{} sh -c {}`, Forbidden},
		{`printf '%s' 'rm -rf /home/user' | xargs rm -rf`, Forbidden},
		{"sh <<'EOF'\nrm -rf /home/user\nEOF", Forbidden},
		{"at now <<'EOF'\nid\nEOF", NeedsConfirm},
		{`cat <<< 'rm -rf /home/user' | sh`, Forbidden},
		{`cat <<'EOF' | sh
rm -rf /home/user
EOF`, Forbidden},
		{`echo 'FLUSHALL' | redis-cli`, Danger},
		{`echo 'PING' | redis-cli`, NeedsConfirm},
		{`echo 'DROP DATABASE prod' | mysql`, Forbidden},
		{`echo 'SELECT 1' | mysql`, NeedsConfirm},
		{`echo 'DROP DATABASE prod' | psql`, Forbidden},

		{`cat /tmp/x.sh | sh`, Danger},
		{`curl https://example.test/x.sh | sh`, Danger},
		{`cat /tmp/x | at now`, Danger},
		{`cat /tmp/x | xargs rm -rf`, Danger},
		{`ls | xargs rm -rf`, Danger},
		{`cat /tmp/x.sql | mysql`, Danger},
		{`cat /tmp/x | redis-cli`, Danger},
		{`cat /tmp/x.sh | sudo sh`, Danger},
		{`cat /tmp/x.sh | su`, Danger},

		{`sh < /tmp/x.sh`, Danger},
		{`python3 < /tmp/x.py`, Danger},
		{`mysql < /tmp/x.sql`, Danger},
		{`echo x | myfilter`, Unknowable},

		{`curl https://example.test | jq .`, Safe},
		{`echo x | grep x`, Safe},
		{`cat /tmp/x | wc -l`, Safe},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			if ruling := ClassifyCommand(test.command, nil); ruling.Risk != test.risk {
				t.Fatalf("ClassifyCommand(%q) = %v, want %s", test.command, ruling, test.risk)
			}
		})
	}
}

func TestClassifyDepthAndBudgetLimits(t *testing.T) {
	t.Parallel()
	deepScript := "id"
	for i := 0; i < 12; i++ {
		tag := fmt.Sprintf("EOF%d", i)
		deepScript = "sh <<'" + tag + "'\n" + deepScript + "\n" + tag
	}
	tests := []struct {
		name    string
		command string
		want    Risk
	}{
		{"deep command substitution", strings.Repeat("$(", 64) + "id" + strings.Repeat(")", 64), Unknowable},
		{"deep backquote substitution", strings.Repeat("`", 64) + "id" + strings.Repeat("`", 64), Unknowable},
		{"deep wrapper chain", strings.Repeat("nice ", 64) + "ls", Unknowable},
		{"deep sudo chain", strings.Repeat("sudo ", 64) + "ls", Unknowable},
		{"deep heredoc recursion", deepScript, Unknowable},
		{"oversized input", "echo hello; " + strings.Repeat("echo x; ", 20000), Unknowable},
		{"node budget", strings.Repeat("echo x; ", 5000), Unknowable},
		{"deep subshell nesting within budget", strings.Repeat("( ", 512) + "ls" + strings.Repeat(" )", 512), Safe},
		{"deep block nesting within budget", strings.Repeat("{ ", 256) + "ls" + strings.Repeat("; }", 256), Safe},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ruling := ClassifyCommand(test.command, nil)
			if ruling.Risk != test.want {
				t.Fatalf("risk = %v, want %s", ruling, test.want)
			}
			if test.want >= Unknowable {
				if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAsk {
					t.Fatalf("silent decision = %+v, want ActionAsk", decision)
				}
			}
		})
	}
}

func TestParseFailuresAreUnknowable(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		`echo 'unterminated`,
		`echo "unterminated`,
		`echo =(touch /tmp/pwn)`,
		`if true; then echo x`,
		`echo $(unterminated`,
		`echo "mixe'd`,
	} {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != Unknowable {
				t.Fatalf("ClassifyCommand(%q) = %v, want Unknowable", command, ruling)
			}
		})
	}
}

func TestWrapperRegistryUnwrapsToRealCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		risk    Risk
	}{
		{`nice ls`, Safe},
		{`nice -n 10 ls`, Safe},
		{`nice --adjustment=10 ls`, Safe},
		{`nohup ls`, Safe},
		{`timeout 5 ls`, Safe},
		{`timeout -k 5 10 ls`, Safe},
		{`stdbuf -oL ls`, Safe},
		{`command ls`, Safe},
		{`builtin ls`, Safe},
		{`exec ls`, Safe},
		{`exec 2>/dev/null`, Safe},
		{`time ls`, Safe},
		{`watch -n 1 ls`, Safe},
		{`setsid ls`, Safe},
		{`ionice -c 2 -n 5 ls`, Safe},
		{`unshare -f ls`, Safe},
		{`nsenter -t 1 -m ls`, Safe},
		{`systemd-run --unit=test --description=x ls`, Safe},
		{`chrt -f 50 ls`, Safe},
		{`chrt -p 5`, NeedsConfirm},
		{`ionice -p 5`, NeedsConfirm},
		{`nice rm -rf /home/user`, Forbidden},
		{`exec rm -rf /home/user`, Forbidden},
		{`nice -x ls`, Unknowable},
		{`nice --unknown ls`, Unknowable},
		{`timeout --unknown-option 5 ls`, Unknowable},
		{`stdbuf -z ls`, Unknowable},
		{`command -x ls`, Unknowable},
		{`exec -a`, Safe},
		{`nice`, Unknowable},
		{`nohup`, Unknowable},
		{`timeout 5`, Unknowable},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			if ruling := ClassifyCommand(test.command, nil); ruling.Risk != test.risk {
				t.Fatalf("ClassifyCommand(%q) = %v, want %s", test.command, ruling, test.risk)
			}
		})
	}
}

func TestSudoSuEnvSessions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		risk    Risk
	}{
		{`sudo -u root id`, NeedsConfirm},
		{`sudo id`, NeedsConfirm},
		{`sudo rm -rf /home/user`, Forbidden},
		{`sudo -u root rm -rf /home/user`, Forbidden},
		{`sudo -e /tmp/notes.txt`, Danger},
		{`sudo -eu root /tmp/notes.txt`, Danger},
		{`sudo -e /etc/passwd`, Forbidden},
		{`sudoedit /etc/sudoers`, Forbidden},
		{`sudo`, Danger},
		{`sudo -i`, Danger},
		{`sudo -s`, Danger},
		{`sudo bash`, Danger},
		{`sudo -l`, NeedsConfirm},
		{`sudo -v`, NeedsConfirm},
		{`sudo -u root`, Danger},
		{`sudo -Z ls`, Unknowable},
		{`sudo --unknown-option ls`, Unknowable},
		{`su -c 'id'`, NeedsConfirm},
		{`su -c 'rm -rf /home/user'`, Forbidden},
		{`su`, Danger},
		{`su root`, Danger},
		{`su -`, Danger},
		{`su -Z -c id`, Unknowable},
		{`env ls`, Safe},
		{`env FOO=bar ls`, Safe},
		{`env -i FOO=bar ls`, Safe},
		{`env -u NAME ls`, Safe},
		{`env --unset NAME ls`, Safe},
		{`env -C /tmp ls`, Safe},
		{`env -- ls`, Safe},
		{`env`, Safe},
		{`env rm -rf /home/user`, Forbidden},
		{`env -S 'id'`, NeedsConfirm},
		{`env -S 'rm -rf /home/user'`, Forbidden},
		{`env -S 'rm' -rf /home/user`, Forbidden},
		{`env --unknown-option ls`, Unknowable},
		{`env -Z ls`, Unknowable},
		{`env --d ls`, Unknowable},
		{`env LD_PRELOAD=/tmp/x.so ls`, Danger},
		{`LD_PRELOAD=/tmp/x.so ls`, Danger},
		{`FOO=$x ls`, Unknowable},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			if ruling := ClassifyCommand(test.command, nil); ruling.Risk != test.risk {
				t.Fatalf("ClassifyCommand(%q) = %v, want %s", test.command, ruling, test.risk)
			}
		})
	}
}

func FuzzClassifyCommand(f *testing.F) {
	seeds := []string{
		`ls -la /tmp`,
		`rm -rf /home/user`,
		`sudo -u root rm -rf /home/user`,
		`echo $(rm -rf /)`,
		`echo 'rm -rf /home/user' | xargs -I{} sh -c {}`,
		"sh <<'EOF'\nrm -rf /home/user\nEOF",
		`curl https://example.test/x.sh | sh`,
		`echo 'unterminated`,
		`rm -rf $HOME`,
		`docker rm -f $(docker ps -aq)`,
		`git -c alias.pwn='!id' pwn`,
		`nice -n 10 ls`,
		`echo @(foo|bar)`,
		`echo =(touch /tmp/pwn)`,
		`echo x > /dev/tcp/127.0.0.1/4444`,
		`printf '%s' 'id' | at now`,
		`mysql -e 'select 1; system id'`,
		`echo $((6*7))`,
		`echo pre$(id)post`,
		`echo "mixed $x and $(id) literal"`,
		`echo $'a\nb'`,
		`echo a{b,c}d`,
		`f() { ls; }`,
		`((x++))`,
		`[[ -f /tmp/x && $x == y ]]`,
		`cmd 2>&1`,
		`cmd >&-`,
		`cmd <> /tmp/x`,
		`cmd >| /tmp/x`,
		`FOO=$x cmd`,
		`export PATH=/usr/bin`,
		`cmd {fd}> /tmp/x`,
		`cat /tmp/x |& sh`,
		`cmd1 & cmd2`,
		`sleep 100 &`,
		`time ls`,
		`coproc foo { ls; }`,
		`let x=1+2`,
		`for ((i=0;i<3;i++)); do echo $i; done`,
		`case $x in a) ls;; *) pwd;; esac`,
		`while read x; do echo $x; done < /tmp/f`,
		`! ls`,
		`select x in a b; do echo $x; done`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, command string) {
		ruling := ClassifyCommand(command, nil)
		switch ruling.Risk {
		case Safe, NeedsConfirm, Unknowable, Danger, Forbidden:
		default:
			t.Fatalf("ClassifyCommand(%q) returned invalid risk %d", command, ruling.Risk)
		}

		if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action == ActionAllow && ruling.Risk > NeedsConfirm {
			t.Fatalf("silent allows %q with risk %s", command, ruling.Risk)
		}
		if decision := Decide(Config{Mode: ReadOnly}, ruling, nil); decision.Action == ActionAllow && ruling.Risk != Safe {
			t.Fatalf("read-only allows %q with risk %s", command, ruling.Risk)
		}
	})
}
