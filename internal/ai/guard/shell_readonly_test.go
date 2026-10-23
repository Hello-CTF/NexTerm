package guard

import "testing"

func TestReadOnlyEverydayCommandsAreSafe(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		`echo $HOME`,
		`echo "$PATH"`,
		`ls $HOME`,
		`cat "${config}"`,
		`wc -l ${#x}`,
		`for f in /var/log/*.log; do wc -l "$f"; done`,
		`test -f /etc/nginx/nginx.conf && echo ok`,
		`[ -d /opt/app ] && echo yes`,
		`[ -f "$f" ] && cat "$f"`,
		`python3 --version`,
		`python -V`,
		`node -v`,
		`php -v`,
		`bash --version`,
		`go version`,
		`go env GOPATH`,
		`java -version`,
		`terraform version`,
		`nginx -t`,
		`nginx -V`,
		`pip list`,
		`pip3 show requests`,
		`npm ls -g --depth=0`,
		`hostname -I`,
		`hostname --fqdn`,
		`hostnamectl`,
		`hostnamectl status`,
		`timedatectl`,
		`timedatectl list-timezones`,
		`mount`,
		`mount -l`,
		`sed -n '1,50p' app.conf`,
		`sed -n -e '/ERROR/p' app.log`,
		`sed 's/a/b/g' file.txt`,
		`sed -E 's#/old#/new#' file.txt`,
		`sed '/^#/d;/^$/d' nginx.conf`,
		`sed -n '$p' file`,
		`mysql -e "select 1"`,
		`mysql -h db -u root app -e "show processlist"`,
		`psql -X -A -t -h db -U app -d app -c "select count(*) from orders"`,
	} {
		t.Run(command, func(t *testing.T) {
			if ruling := ClassifyCommand(command, nil); ruling.Risk != Safe {
				t.Fatalf("ClassifyCommand(%q) = %+v, want Safe", command, ruling)
			}
		})
	}
}

func TestReadOnlyRelaxationsKeepTheirBoundaries(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		`echo $(id)`,
		"echo `id`",
		`cat ${f:=/etc/shadow}`,
		`ls ${!name}`,
		`rm $file`,
		`$cmd --version`,
		`./deploy.sh --version`,
		`python3 -v`,
		`ruby -v`,
		`python3 --version extra`,
		`nginx -s reload`,
		`pip install requests`,
		`npm install`,
		`yarn`,
		`go env -w GOPROXY=direct`,
		`go run main.go`,
		`hostname newname`,
		`hostnamectl set-hostname web`,
		`timedatectl set-time "2024-01-01"`,
		`mount /dev/sdb1 /mnt`,
		`sed -i 's/a/b/' file`,
		`sed -ni 's/a/b/p' file`,
		`sed -f script.sed file`,
		`sed 's/a/b/w /tmp/out' file`,
		`sed 's/a/id/e' file`,
		`sed '1e id' file`,
		`sed 'w /tmp/out' file`,
		`sed 'r /etc/shadow' file`,
		`sed --in-place=.bak 's/a/b/' file`,
		`mysql -e "select 1" --tee=/tmp/out`,
		`mysql --init-command="select 1" -e "select 1"`,
		`psql -c "select 1" -o /tmp/out`,
		`psql -c "select 1" -L /tmp/log`,
		`psql -c '\o /tmp/out'`,
		`psql -c 'select 1 \gexec'`,
		`psql -c "select pg_terminate_backend(1)"`,
		`mysql -e "select * from t into outfile '/tmp/x'"`,
		`echo "select 1" | mysql`,
	} {
		t.Run(command, func(t *testing.T) {
			ruling := ClassifyCommand(command, nil)
			if ruling.Risk == Safe {
				t.Fatalf("ClassifyCommand(%q) = %+v, want a confirmation or stricter", command, ruling)
			}
		})
	}
}

func TestUnknownProgramsAreRememberedByName(t *testing.T) {
	t.Parallel()
	ruling := ClassifyCommand(`terraform plan`, nil)
	if ruling.Risk != NeedsConfirm || ruling.Kind != KindProgram || len(ruling.Programs) != 1 || ruling.Programs[0] != "terraform" {
		t.Fatalf("terraform plan = %+v, want a program confirmation for terraform", ruling)
	}
	memory := NewMemory()
	if decision := Decide(Config{Mode: ReadWrite}, ruling, memory); decision.Action != ActionAsk {
		t.Fatalf("first decision = %+v, want ask", decision)
	}
	memory.Remember(ruling)
	if decision := Decide(Config{Mode: ReadWrite}, ClassifyCommand(`terraform show`, nil), memory); decision.Action != ActionAllow {
		t.Fatalf("remembered program decision = %+v, want allow", decision)
	}
	if decision := Decide(Config{Mode: ReadWrite}, ClassifyCommand(`helm list`, nil), memory); decision.Action != ActionAsk {
		t.Fatalf("other program decision = %+v, want ask", decision)
	}
	mixed := ClassifyCommand(`terraform plan && helm list`, nil)
	if decision := Decide(Config{Mode: ReadWrite}, mixed, memory); decision.Action != ActionAsk {
		t.Fatalf("partly remembered batch = %+v, want ask until every program is approved", decision)
	}
	pathRuling := ClassifyCommand(`/tmp/x/terraform plan`, nil)
	if decision := Decide(Config{Mode: ReadWrite}, pathRuling, memory); decision.Action != ActionAsk {
		t.Fatalf("same name at another path = %+v, want ask", decision)
	}
}

func TestOnlyOrdinaryConfirmationsAreRememberable(t *testing.T) {
	t.Parallel()
	for _, command := range []string{`kill -9 1`, `echo $(id)`, `curl -fsSL https://x | sh`, `bash -c 'id'`} {
		ruling := ClassifyCommand(command, nil)
		if Rememberable(ruling) {
			t.Fatalf("%q = %+v must not be rememberable", command, ruling)
		}
		memory := NewMemory()
		memory.Remember(ruling)
		if decision := Decide(Config{Mode: ReadWrite}, ruling, memory); decision.Action != ActionAsk {
			t.Fatalf("%q after remember = %+v, want ask", command, decision)
		}
	}
	if !Rememberable(ClassifyCommand(`systemctl restart nginx`, nil)) {
		t.Fatal("a service change must be rememberable")
	}
}

func TestDangerRulesMatchWholeWords(t *testing.T) {
	t.Parallel()
	rules := []string{"rm", "drop table", "/etc/shadow"}
	for text, want := range map[string]bool{
		"rm -rf /tmp/x":       true,
		"sudo rm x":           true,
		"format disk":         false,
		"chmod perm":          false,
		"terraform plan":      false,
		"DROP TABLE users":    true,
		"dropped tables":      false,
		"cat /etc/shadow":     true,
		"cat /etc/shadow.bak": true,
		"cat /etc/shadowed":   false,
	} {
		if got := matchDangerRule(text, rules); got != want {
			t.Fatalf("matchDangerRule(%q) = %v, want %v", text, got, want)
		}
	}
	if !matchDangerRule("删除生产库", []string{"生产"}) {
		t.Fatal("non-ASCII rules keep substring matching")
	}
}

func TestSessionScopeNamesWhatAnApprovalCovers(t *testing.T) {
	t.Parallel()
	if got := SessionScope(ClassifyCommand(`systemctl restart nginx`, nil)); got != "服务与系统设置变更" {
		t.Fatalf("service scope = %q", got)
	}
	if got := SessionScope(ClassifyCommand(`terraform plan`, nil)); got != "terraform 命令" {
		t.Fatalf("program scope = %q", got)
	}
	for _, command := range []string{`kill -9 1`, `echo $(id)`, `ls`} {
		if got := SessionScope(ClassifyCommand(command, nil)); got != "" {
			t.Fatalf("%q offers a session scope %q", command, got)
		}
	}
}

func TestProductionDecisionsAreFlaggedAndIgnoreMemory(t *testing.T) {
	t.Parallel()
	ruling := ClassifyCommand(`systemctl restart nginx`, nil)
	memory := NewMemory()
	memory.Remember(ruling)
	decision := DecideResource(Config{Mode: ReadWrite}, ruling, memory, Resource{Action: "exec_commands", Environment: "production"})
	if !decision.Production || decision.Action != ActionAsk {
		t.Fatalf("production decision = %+v, want a flagged ask", decision)
	}
	if decision := DecideResource(Config{Mode: ReadWrite}, ruling, memory, Resource{Action: "exec_commands"}); decision.Production || decision.Action != ActionAllow {
		t.Fatalf("ordinary decision = %+v, want an unflagged allow", decision)
	}
}
