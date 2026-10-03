package guard

import "testing"

// R9: regression coverage for the independent review round. Every construct
// class is pinned with the payload variant, the preserved benign form and the
// decision-layer consequence, so Silent can never auto-execute the dangerous
// form and the read-only forms stay usable.

// R9 P1-1: [[ ... ]] operands and case patterns undergo expansion:
// substitutions inside them execute and dynamic words cannot be proven.
func TestR9TestClauseAndCasePatterns(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`[[ $(rm -rf /) ]]`,
		`[[ -f $(rm -rf /) ]]`,
		`[[ $x == $(rm -rf /) ]]`,
		`[[ $(rm -rf /) == y ]]`,
		`case x in $(rm -rf /)) ls;; esac`,
		`case $(rm -rf /) in a) ls;; esac`,
		`case x in a|$(rm -rf /)) ls;; esac`,
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
		`[[ $x ]]`,
		`[[ -f $x ]]`,
		`[[ $x == y ]]`,
		`[[ -n ${x:-$(id)} ]]`,
		`case x in $x) ls;; esac`,
		`case ${x} in a) ls;; esac`,
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
	safe := []string{
		`[[ -f /tmp/x ]]`,
		`[[ -f /tmp/x && -d /tmp/y ]]`,
		`[[ ! -e /tmp/x ]]`,
		`case x in a) ls;; esac`,
		`case x in a|b) ls;; esac`,
		`case *.log in *.log) ls;; esac`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

// R9 P1-2: pipes and here-documents feed compound statements; the stdin
// source propagates into subshells, blocks, conditionals, loops and case
// bodies instead of being dropped.
func TestR9CompoundStatementStdin(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`echo 'DROP DATABASE prod' | (mysql)`,
		`echo 'DROP DATABASE prod' | { mysql; }`,
		`echo 'rm -rf /home/user' | (sh)`,
		`echo 'rm -rf /home/user' | if true; then sh; fi`,
		`echo 'rm -rf /home/user' | while true; do sh; done`,
		`echo 'rm -rf /home/user' | case x in a) sh;; esac`,
		`echo 'rm -rf /home/user' | for f in a; do sh; done`,
		`(at now) <<< 'rm -rf /'`,
		`echo 'rm -rf /' | { at now; }`,
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
		`{ redis-cli; } <<< 'FLUSHALL'`,
		`echo 'FLUSHALL' | if true; then redis-cli; fi`,
		`(redis-cli) <<< 'FLUSHALL'`,
		`echo 'FLUSHALL' | { redis-cli; }`,
		`cat /tmp/x | { redis-cli; }`,
		`coproc foo { mysql; }`,
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
		`echo 'id' | (sh)`,
		`echo 'id' | { sh; }`,
		`(at now) <<< 'id'`,
		`echo 'SELECT 1' | (mysql)`,
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
		`(ls)`,
		`{ ls; pwd; }`,
		`echo x | (cat)`,
		`{ cat; } <<< 'hello'`,
		`coproc foo { ls; }`,
		`if true; then ls; fi`,
		`while true; do ls; done`,
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

// R9 P1-3: the determinable pipe producer model matches real printf
// (option handling, format reuse) and real cat (no file operands, last
// stdin-setting redirect wins).
func TestR9DeterminableProducers(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`printf '%s' 'r' 'm -rf /' | sh`,
		`printf -- 'rm -rf /' | sh`,
		`printf '%s\n' 'rm -rf /home/user' | sh`,
		`printf '%s' 'rm' ' -rf /home/user' | sh`,
		`printf '%s %s' 'rm -rf' '/home/user' | sh`,
		`printf 'rm -rf /' | sh`,
		`printf '%s' 'rm -rf /' 'home' | sh`,
	}
	for _, command := range forbidden {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != Forbidden {
				t.Fatalf("ClassifyCommand(%q) = %v, want Forbidden", command, ruling)
			}
		})
	}
	danger := []string{
		"cat /tmp/x <<'EOF' | sh\nid\nEOF",
		"cat <<'EOF' < /tmp/x | sh\nid\nEOF",
		"cat - /tmp/x <<'EOF' | sh\nid\nEOF",
		"cat /tmp/x - <<'EOF' | sh\nid\nEOF",
		`printf '%b' 'rm -rf /' | sh`,
	}
	for _, command := range danger {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Danger {
				t.Fatalf("ClassifyCommand(%q) = %v, want Danger", command, ruling)
			}
		})
	}
	confirm := []string{
		"cat <<'EOF' | sh\nid\nEOF",
		`cat <<< 'id' | sh`,
		`printf -v x '%s' 'rm -rf /' | sh`,
		`printf 'x' a b | sh`,
		`printf '%s' 'id' | sh`,
		"cat - <<'EOF' | sh\nid\nEOF",
		`printf '%d' '0' | sh`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
	safe := []string{
		"cat <<'EOF'\nhello $x\nEOF",
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

// R9 P1-4: xargs parses the whole input stream with its quoting and
// delimiter rules and batches words into invocations; -n batches, -L/-s
// fail closed, unparseable input fails closed.
func TestR9XargsStreamSemantics(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"xargs rm <<'EOF'\n-rf\n/\nEOF",
		`echo '"-rf" "/"' | xargs rm`,
		`printf '%s' '-rf /' | xargs rm`,
		`echo '"/"' | xargs rm -rf`,
		"printf 'a \"b c\"\\n' | xargs rm -rf /",
		`echo 'rm -rf /' | xargs -I{} sh -c {}`,
		`echo '"rm -rf /"' | xargs -I{} sh -c {}`,
		`echo 'rm -rf /' | xargs -i sh -c {}`,
		`printf '%s' '-rf:/' | xargs -d: rm`,
		`echo 'a b c' | xargs -n2 rm -rf /`,
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
		`echo x | xargs -s 100 rm`,
		`echo '"a' | xargs rm`,
		`echo 'a\' | xargs rm`,
		`echo x | xargs -0 -d: rm`,
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
	// -L is precisely modeled per logical line (see R10).
	for _, command := range []string{`echo x | xargs -L2 rm`, `echo x | xargs --max-lines=2 rm`} {
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
	unknowable := []string{
		`echo x | xargs -n 0 rm`,
		`echo x | xargs -n abc rm`,
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
		`printf '' | xargs rm`,
		`echo x | xargs rm`,
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
		`echo x | xargs`,
		`echo 'a:b' | xargs -d: echo`,
		`echo x | xargs -n1 echo`,
		`printf '' | xargs -r rm`,
		`printf '' | xargs --no-run-if-empty rm`,
		`echo x | xargs -i echo {}`,
		`echo 'a b' | xargs printf '%s'`,
		`echo 'rm -rf /home/user' | xargs`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

// R9 P1-5: GNU tar's -I short option runs an external filter program just
// like --use-compress-program.
func TestR9TarShortExternalCommand(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`tar -tf archive.tar -I 'rm -rf /'`,
		`tar -xf archive.tar -I 'rm -rf /'`,
		`tar -tf archive.tar -I'rm -rf /'`,
		`tar -xf archive.tar -I 'sh -c "rm -rf /"'`,
		`tar -cf out.tar -I 'rm -rf /' /tmp/x`,
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
		`tar -tf archive.tar -I 'gzip -d'`,
		`tar -tf archive.tar --use-compress-program='gzip -d'`,
		`tar -tf archive.tar -I 'cat'`,
	}
	for _, command := range confirm {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != NeedsConfirm {
				t.Fatalf("ClassifyCommand(%q) = %v, want NeedsConfirm", command, ruling)
			}
		})
	}
	if ruling := ClassifyCommand(`tar -tf archive.tar -I`, nil); ruling.Risk != Unknowable {
		t.Fatalf("tar -I without value = %v, want Unknowable", ruling)
	}
	safe := []string{
		`tar -tf archive.tar`,
		`tar -tfI archive.tar`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

// R9 P1-6: kubectl inherited globals are consumed before subcommand
// dispatch; ssh consumes its full value/flag tables so options like -R/-D
// never shift the remote command, and -o accepts the space-separated
// ssh_config syntax.
func TestR9KubectlSSHGlobalOptions(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`kubectl --context prod exec pod -- rm -rf /`,
		`kubectl --context=prod exec pod -- rm -rf /`,
		`kubectl --kubeconfig /tmp/k exec pod -- rm -rf /`,
		`kubectl -n prod exec pod -- rm -rf /`,
		`kubectl --context prod exec pod -- sh -c 'rm -rf /'`,
		`ssh -R 9000:localhost:9000 host rm -rf /`,
		`ssh -R9000:localhost:9000 host rm -rf /`,
		`ssh -D 1080 host rm -rf /`,
		`ssh -p 2222 -R 9000:localhost:9000 host rm -rf /`,
		`ssh -o 'ProxyCommand rm -rf /' host`,
		`ssh -o 'ProxyCommand=rm -rf /' host`,
		`ssh -o 'LocalCommand rm -rf /' host`,
		`ssh -o 'RemoteCommand rm -rf /' host`,
		`ssh -o 'ProxyCommand = rm -rf /' host`,
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
		`ssh -o 'ProxyCommand rm -rf /tmp/x' host`,
		`ssh -o 'ProxyCommand id' host`,
		`ssh -o 'LocalCommand id' host`,
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
	unknowable := []string{
		`kubectl --unknown-global get pods`,
		`kubectl --cont prod get pods`,
		`kubectl -Z get pods`,
		`ssh --unknown-global host`,
		`ssh -Z host`,
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
		`ssh -R 9000:localhost:9000 host`,
		`ssh -o ProxyCommand=none host`,
		`ssh host`,
		`kubectl exec pod -- ls`,
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
		`kubectl --context prod get pods`,
		`kubectl --context=prod get pods`,
		`kubectl -n prod get pods`,
		`kubectl --kubeconfig /tmp/k get pods`,
		`kubectl -v 5 get pods`,
		`kubectl get pods -A`,
		`kubectl --namespace prod logs web`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

// R9 P2-1: command substitutions nested inside parameter and arithmetic
// expansions execute and must be aggregated; the dynamic outer construct
// keeps its Unknowable floor when the inner content is benign.
func TestR9NestedExpansionSubstitutions(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`echo ${x:-$(rm -rf /)}`,
		`echo ${x:-` + "`rm -rf /`" + `}`,
		`echo ${x:$(rm -rf /):1}`,
		`echo ${x/$(rm -rf /)/y}`,
		`echo ${a[$(rm -rf /)]}`,
		`echo $(( $(rm -rf /) ))`,
		`echo $(( 1 + $(rm -rf /) ))`,
		`(( $(rm -rf /) ))`,
		`let $(rm -rf /)`,
		`FOO[$(rm -rf /)]=x`,
		`FOO=($(rm -rf /))`,
		`FOO=(a $(rm -rf /) b)`,
		`echo "${x:-$(rm -rf /)}"`,
		`echo pre${x:-$(rm -rf /)}post`,
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
		`echo ${x:-default}`,
		`echo ${x:-$(id)}`,
		`echo $(( $(id) ))`,
		`echo $((6*7))`,
		`((x++))`,
		`let x=1+2`,
		`FOO[a]=x`,
		`echo ${a[$i]}`,
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

// R9 P2-2: attached -T-/-d- are uploads and POSTs, not the stdout exemption.
func TestR9CurlAttachedStdinForms(t *testing.T) {
	t.Parallel()
	confirm := []string{
		`curl -T- https://example.test/upload`,
		`curl -d- https://example.test/submit`,
		`curl -F- https://example.test/form`,
		`curl -T - https://example.test/upload`,
		`curl -d - https://example.test/submit`,
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
		`curl -o- https://example.test`,
		`curl -D- https://example.test`,
		`curl -c- https://example.test`,
		`curl -G -d- https://example.test`,
		`curl -sG -d foo=bar https://example.test`,
		`curl https://example.test`,
	}
	for _, command := range safe {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %v, want Safe", command, ruling)
			}
		})
	}
}

// R9 P2-3: tool-call parse failures and unknown tools are Unknowable, never
// Silent-pre-approved.
func TestR9ToolCallFailClosed(t *testing.T) {
	t.Parallel()
	unknowable := []struct {
		name string
		args string
	}{
		{"exec_commands", `{`},
		{"exec_commands", `{"commands":"x"}`},
		{"exec_commands", `{"commands":{}}`},
		{"exec_commands", `{"commands":null}`},
		{"mystery_tool", `{}`},
		{"mystery_tool", ``},
	}
	for _, test := range unknowable {
		t.Run(test.name+"/"+test.args, func(t *testing.T) {
			ruling := ClassifyTool(test.name, []byte(test.args), Config{})
			if ruling.Risk != Unknowable {
				t.Fatalf("ClassifyTool(%s, %q) = %v, want Unknowable", test.name, test.args, ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAsk {
				t.Fatalf("silent decision = %+v, want ActionAsk", decision)
			}
			if decision := Decide(Config{Mode: ReadOnly}, ruling, nil); decision.Action != ActionDeny {
				t.Fatalf("read-only decision = %+v, want ActionDeny", decision)
			}
		})
	}
	if ruling := ClassifyTool("exec_commands", []byte(`{"commands":[]}`), Config{}); ruling.Risk != Safe {
		t.Fatalf("empty command list = %v, want Safe", ruling)
	}
	if ruling := ClassifyTool("exec_commands", []byte(`{"commands":["ls -la /tmp"]}`), Config{}); ruling.Risk != Safe {
		t.Fatalf("benign command list = %v, want Safe", ruling)
	}
	if ruling := ClassifyTool("exec_commands", []byte(`{"commands":["rm -rf /home/user"]}`), Config{}); ruling.Risk != Forbidden {
		t.Fatalf("forbidden command list = %v, want Forbidden", ruling)
	}
}

// R9 preserved boundaries: the fixes must not move the established benign
// rulings.
func TestR9PreservedBoundaries(t *testing.T) {
	t.Parallel()
	safe := []string{
		`ls -la /tmp`,
		`git status --short`,
		`git --no-pager log`,
		`git push origin main`,
		`curl https://example.test`,
		`echo x | xargs`,
		`wget -qO- https://example.test`,
		`psql -c 'SELECT 1'`,
		`nice ls`,
		`echo 'id' | sh`,
		`sudo -u root id`,
		`echo hi | at now`,
		`crontab -l`,
		`git clean -n`,
		`docker ps -a`,
		`touch /tmp/x`,
		`echo pwn > /tmp/x`,
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

// TestR9XargsWordParsing pins the stream parsing directly: -0 and -d take
// items literally, the default mode applies quoting rules, and unparseable
// input reports false.
func TestR9XargsWordParsing(t *testing.T) {
	t.Parallel()
	words, ok := xargsInputWords(wrapperScan{nulMode: true}, "-rf\x00/\x00")
	if !ok || len(words) != 2 || words[0] != "-rf" || words[1] != "/" {
		t.Fatalf("nul words = %q ok=%v", words, ok)
	}
	words, ok = xargsInputWords(wrapperScan{hasDelimiter: true, delimiter: ":"}, "-rf:/")
	if !ok || len(words) != 2 || words[0] != "-rf" || words[1] != "/" {
		t.Fatalf("delimiter words = %q ok=%v", words, ok)
	}
	words, ok = xargsWords("\"-rf\" '/'\n")
	if !ok || len(words) != 2 || words[0] != "-rf" || words[1] != "/" {
		t.Fatalf("quoted words = %q ok=%v", words, ok)
	}
	words, ok = xargsWords("a 'b c' d\\ e\n")
	if !ok || len(words) != 3 || words[0] != "a" || words[1] != "b c" || words[2] != "d e" {
		t.Fatalf("escaped words = %q ok=%v", words, ok)
	}
	if _, ok = xargsWords("\"unterminated"); ok {
		t.Fatalf("unterminated quote parsed as ok")
	}
	if _, ok = xargsWords("trailing\\"); ok {
		t.Fatalf("trailing backslash parsed as ok")
	}
	line, ok := xargsProcessedLine("\"rm -rf /\"")
	if !ok || line != "rm -rf /" {
		t.Fatalf("processed line = %q ok=%v", line, ok)
	}
}

// R9 sibling sweep: tar -F/--info-script and the scp/sftp/rsync program
// execution options are the same external-command class as -I and ssh
// ProxyCommand.
func TestR9ExternalCommandSiblings(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`tar -cf out.tar -F 'rm -rf /' /tmp/x`,
		`tar -cf out.tar --info-script='rm -rf /' /tmp/x`,
		`tar -cf out.tar --info-script 'rm -rf /' /tmp/x`,
		`scp -o 'ProxyCommand rm -rf /' host:/tmp/x /tmp/y`,
		`sftp -o 'ProxyCommand rm -rf /' host`,
		`scp -S 'rm -rf /' host:/tmp/x /tmp/y`,
		`rsync -e 'rm -rf /' /tmp/x host:/tmp/y`,
		`rsync --rsh='rm -rf /' /tmp/x host:/tmp/y`,
		`rsync --rsync-path='rm -rf /' /tmp/x host:/tmp/y`,
		`rsync -e 'sh -c "rm -rf /"' /tmp/x host:/tmp/y`,
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
		`sftp -b /tmp/payload host`,
		`sftp -b/tmp/payload host`,
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
		`scp host:/tmp/x /tmp/y`,
		`sftp host`,
		`rsync /tmp/x /tmp/y`,
		`scp -o ProxyCommand=none host:/tmp/x /tmp/y`,
		`rsync -e 'ssh -p 2222' /tmp/x host:/tmp/y`,
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
