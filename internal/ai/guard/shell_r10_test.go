package guard

import "testing"

func TestR10PrintfOptionEnd(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`printf -- '-v x; rm -rf /home/user' | sh`,
		`printf -- '-x; rm -rf /home/user' | sh`,
		`printf -- 'rm -rf /home/user' | sh`,
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
		`printf -- '%s' 'id' | sh`,
		`printf -- 'id' | sh`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAllow {
				t.Fatalf("silent decision = %+v, want ActionAllow", decision)
			}
		})
	}
	safe := []string{
		`printf -- '-v' | cat`,
		`printf '%s' 'id' | cat`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

func TestR10XargsLineBatches(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`printf 'id\n"rm -rf /home/user"\n' | xargs -l sh -c`,
		`printf 'id\n"rm -rf /home/user"\n' | xargs -L1 sh -c`,
		`printf 'id\n"rm -rf /home/user"\n' | xargs --max-lines=1 sh -c`,
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
		`printf 'id\n"rm -rf /home/user"\n' | xargs -l -n1 sh -c`,
		`echo x | xargs -L2 -n2 rm`,
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
	if ruling := ClassifyCommand(`echo x | xargs -L abc rm`, nil); ruling.Risk != Unknowable {
		t.Fatalf("xargs -L abc = %v, want Unknowable", ruling)
	}
	if ruling := ClassifyCommand(`printf 'id\n"rm -rf /home/user"\n' | xargs -l2 sh -c`, nil); ruling.Risk != Unknowable {
		t.Fatalf("xargs -l2 = %v, want Unknowable", ruling)
	}
	confirm := []string{
		`printf 'id\nrm -rf /home/user\n' | xargs -l sh -c`,
		`printf 'id\nls\n' | xargs -l sh -c`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAllow {
				t.Fatalf("silent decision = %+v, want ActionAllow", decision)
			}
		})
	}
	safe := []string{
		`echo x | xargs echo`,
		`printf 'a b\nc d\n' | xargs -L1 echo`,
		`printf 'a\nb\nc\n' | xargs -L2 echo`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

func TestR10TarLongOptionAbbreviations(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`tar -tf archive.tar --use-compress-prog='rm -rf /home/user'`,
		`tar -tf archive.tar --use-compress-program='rm -rf /home/user'`,
		`tar -tf archive.tar --to-comm='rm -rf /home/user'`,
		`tar -tf archive.tar --info-scr='rm -rf /home/user'`,
		`tar -tf archive.tar --checkpoint-act='exec=rm -rf /home/user'`,
		`tar -xf archive.tar --use-compress-prog 'rm -rf /home/user'`,
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
	unknowable := []string{
		`tar -tf archive.tar --use-compress-prog`,
		`tar -tf archive.tar --unknown-option`,
		`tar -tf archive.tar --d /tmp`,
		`tar --lis --file archive.tar`,
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
		`tar -tf archive.tar`,
		`tar --list --file archive.tar`,
		`tar -tf archive.tar --exclude-vcs`,
		`tar -tf archive.tar --wildcards 'src/*'`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

func TestR10WatchShellSemantics(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`watch -n1 'rm -rf /home/user'`,
		`watch 'rm -rf /home/user'`,
		`watch rm -rf /home/user`,
		`watch -n 1 'rm -rf /home/user'`,
		`watch --interval 1 'rm -rf /home/user'`,
		`watch -x rm -rf /home/user`,
		`watch --exec rm -rf /home/user`,
		`watch -n1 'id; rm -rf /home/user'`,
		`watch -n1 'echo x | rm -rf /home/user'`,
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

		`watch -n1 sh -c 'rm -rf /home/user'`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAllow {
				t.Fatalf("silent decision = %+v, want ActionAllow", decision)
			}
		})
	}
	safe := []string{
		`watch ls`,
		`watch -n1 ls`,
		`watch 'ls -la /tmp'`,
		`watch -x ls`,
		`watch -n1 'id'`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
	if ruling := ClassifyCommand(`watch -Z ls`, nil); ruling.Risk != Unknowable {
		t.Fatalf("watch unknown option = %v, want Unknowable", ruling)
	}
}

func TestR10ExecOptionAlignment(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`kubectl exec pod -i -- rm -rf /home/user`,
		`kubectl exec pod -it -- rm -rf /home/user`,
		`kubectl exec pod -q -- rm -rf /home/user`,
		`kubectl exec pod -c container -- rm -rf /home/user`,
		`kubectl exec pod --context prod -- rm -rf /home/user`,
		`kubectl exec -i pod -- rm -rf /home/user`,
		`kubectl exec pod rm -rf /home/user`,
		`docker exec c -i rm -rf /home/user`,
		`docker exec c -it rm -rf /home/user`,
		`docker exec -i c rm -rf /home/user`,
		`docker exec c --privileged rm -rf /home/user`,
		`docker exec c -- rm -rf /home/user`,
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
	unknowable := []string{
		`kubectl exec pod -Z -- rm -rf /home/user`,
		`kubectl exec --unknown-opt pod -- rm`,
		`docker exec c -Z rm`,
		`docker exec c --unknown-opt rm`,
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
	confirm := []string{
		`kubectl exec pod -i -- ls`,
		`kubectl exec pod -it -- sh -c 'id'`,
		`docker exec c -i ls`,
		`docker exec c -it sh -c 'id'`,
		`docker exec c ls`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAllow {
				t.Fatalf("silent decision = %+v, want ActionAllow", decision)
			}
		})
	}
}

func TestR10SSHConfigExecutableKeywords(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`ssh -o 'KnownHostsCommand rm -rf /home/user' host`,
		`ssh -o 'KnownHostsCommand=rm -rf /home/user' host`,
		`ssh -o 'XAuthLocation rm -rf /home/user' host`,
		`scp -o 'KnownHostsCommand rm -rf /home/user' host:/tmp/x /tmp/y`,
		`sftp -o 'KnownHostsCommand rm -rf /home/user' host`,
		`ssh -o 'KnownHostsCommand sh -c "rm -rf /home/user"' host`,
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
		`ssh -o 'KnownHostsCommand id' host`,
		`ssh -o 'Include /tmp/ssh-config' host`,
		`scp -o 'Include /tmp/ssh-config' host:/tmp/x /tmp/y`,
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
		`ssh -o 'KnownHostsCommand none' host`,
		`ssh -o 'StrictHostKeyChecking=no' host`,
		`ssh host`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAllow {
				t.Fatalf("silent decision = %+v, want ActionAllow", decision)
			}
		})
	}
}

func TestR10CStyleLoopTraversal(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`for ((i=$(rm -rf /home/user);i<1;i++)); do :; done`,
		`for ((i=0;i<$(rm -rf /home/user);i++)); do :; done`,
		`for ((i=0;i<1;i=$(rm -rf /home/user))); do :; done`,
		`for ((i=$(id);i<1;i++)); do rm -rf /home/user; done`,
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
	unknowable := []string{
		`for ((i=0;i<3;i++)); do echo $i; done`,
		`for ((i=$(id);i<1;i++)); do :; done`,
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
		`for f in a b; do echo x; done`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

func TestR10ToolArgumentFailClosed(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"read_file", "write_file", "exec_commands", "send_keys", "db_query", "docker_exec", "redis_command", "ask_user"} {
		for _, args := range []string{"null", `"text"`, `[1,2]`, `42`} {
			ruling := ClassifyTool(name, []byte(args), Config{})
			if ruling.Risk != Unknowable {
				t.Fatalf("ClassifyTool(%s, %q) = %v, want Unknowable", name, args, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAsk {
				t.Fatalf("silent decision = %+v, want ActionAsk", decision)
			}
		}
	}

	if ruling := ClassifyTool("read_file", nil, Config{}); ruling.Risk != Safe {
		t.Fatalf("read_file without args = %v, want Safe", ruling)
	}
	if ruling := ClassifyTool("read_file", []byte(`{}`), Config{}); ruling.Risk != Safe {
		t.Fatalf("read_file empty object = %v, want Safe", ruling)
	}
}

func TestR10PreservedBoundaries(t *testing.T) {
	t.Parallel()
	safe := []string{
		`ls -la /tmp`,
		`git status --short`,
		`git --no-pager log`,
		`git push origin main`,
		`curl https://example.test`,
		`echo x | xargs`,
		`echo x | xargs ls`,
		`wget -qO- https://example.test`,
		`psql -c 'SELECT 1'`,
		`nice ls`,
		`watch ls`,
		`echo 'id' | sh`,
		`sudo -u root id`,
		`echo hi | at now`,
		`crontab -l`,
		`git clean -n`,
		`docker ps -a`,
		`touch /tmp/x`,
		`echo pwn > /tmp/x`,
		`tar -tf archive.tar`,
		`kubectl get pods -A`,
		`ssh host`,
	}
	for _, command := range safe {
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

func TestR10BuildToolExecution(t *testing.T) {
	t.Parallel()
	danger := []string{
		`make`,
		`make build`,
		`make -j4`,
		`make -C /tmp/project`,
		`make --eval='$(shell rm -rf /home/user)'`,
		`ninja`,
		`ninja all`,
		`cmake --build /tmp/project`,
		`cmake --install /tmp/project`,
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
		`make -f /tmp/project/Makefile`,
		`make --file /tmp/project/Makefile build`,
		`make -fMakefile build`,
		`make -t -f /tmp/project/Makefile`,
		`ninja -f /tmp/project/build.ninja`,
		`cmake /tmp/project`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAllow {
				t.Fatalf("silent decision = %+v, want ActionAllow", decision)
			}
		})
	}
	if ruling := ClassifyCommand(`ninja -t clean`, nil); ruling.Risk != NeedsConfirm {
		t.Fatalf("ninja -t clean = %v, want NeedsConfirm", ruling)
	}
	unknowable := []string{
		`make -Z build`,
		`make --unknown-option build`,
		`ninja -Z`,
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
}

func TestR10GitDoubleDash(t *testing.T) {
	t.Parallel()
	for _, command := range []string{`git -- status`, `git -- log --oneline`} {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
	if ruling := ClassifyCommand(`git --unknown-global status`, nil); ruling.Risk != Unknowable {
		t.Fatalf("git unknown global = %v, want Unknowable", ruling)
	}
}
