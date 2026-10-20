package services

import (
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

const ssSample = `State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process
LISTEN 0      128          0.0.0.0:22        0.0.0.0:*    users:(("sshd",pid=812,fd=3))
LISTEN 0      100                *:8080             *:*    users:(("nginx",pid=1201,fd=6),("nginx",pid=1202,fd=7))
LISTEN 0      128             [::]:443          [::]:*    users:(("envoy",pid=1440,fd=12))
LISTEN 0      50                 :::9000            :::*
`

const psSample = `    PID USER     %CPU %MEM ELAPSED ARGS
      1 root      0.0  0.1 10-01:02:03 /sbin/init
    812 root      0.1  0.4 02:11:09 /usr/sbin/sshd -D
   1201 www-data  1.2  2.3 01:04:05 nginx: master process /usr/sbin/nginx
   1440 envoy     4.5  8.9 00:10:20 /usr/local/bin/envoy -c /etc/envoy.yaml
`

func TestParseSSAndPSVariants(t *testing.T) {
	t.Parallel()
	listeners := ParseSS(ssSample)
	if len(listeners) != 5 {
		t.Fatalf("ss listeners = %+v", listeners)
	}
	if listeners[0].Address != "0.0.0.0" || listeners[0].Port != 22 || listeners[0].PID != 812 || listeners[0].Program != "sshd" {
		t.Fatalf("ssh listener = %+v", listeners[0])
	}
	if listeners[1].PID != 1201 || listeners[2].PID != 1202 || listeners[1].Port != 8080 {
		t.Fatalf("multi-process listener = %+v", listeners[1:3])
	}
	if listeners[3].Address != "::" || listeners[3].Port != 443 {
		t.Fatalf("IPv6 listener = %+v", listeners[3])
	}
	if listeners[4].Address != "::" || listeners[4].Port != 9000 || listeners[4].PID != 0 {
		t.Fatalf("unprivileged listener = %+v", listeners[4])
	}

	processes := ParsePS(psSample)
	if len(processes) != 4 {
		t.Fatalf("processes = %+v", processes)
	}
	if processes[0].Elapsed != "10-01:02:03" || processes[0].Args != "/sbin/init" {
		t.Fatalf("long elapsed process = %+v", processes[0])
	}
	if processes[1].User != "root" || processes[1].CPU != "0.1" || processes[1].Memory != "0.4" || processes[1].Name != "sshd" {
		t.Fatalf("sshd process = %+v", processes[1])
	}
	if processes[2].Args != "nginx: master process /usr/sbin/nginx" {
		t.Fatalf("args with spaces = %+v", processes[2])
	}
}

func TestParseNetstatLinuxAndBusyBoxVariants(t *testing.T) {
	t.Parallel()
	output := `Active Internet connections (only servers)
Proto Recv-Q Send-Q Local Address           Foreign Address         State       PID/Program name
tcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN      812/sshd
tcp6       0      0 :::443                  :::*                    LISTEN      1440/envoy
tcp        0      0 127.0.0.1:6379          0.0.0.0:*               LISTEN      -
`
	listeners := ParseNetstat(output)
	if len(listeners) != 3 {
		t.Fatalf("netstat listeners = %+v", listeners)
	}
	if listeners[0].PID != 812 || listeners[0].Program != "sshd" || listeners[1].Address != "::" || listeners[2].PID != 0 {
		t.Fatalf("netstat mapping = %+v", listeners)
	}
}

func probeOutput(tool, listeners, processes string) string {
	return "__NEXTERM_LISTENER_BEGIN__\n__NEXTERM_TOOL__" + tool + "\n" + listeners +
		"\n__NEXTERM_LISTENER_END__:0\n__NEXTERM_PS_BEGIN__\n" + processes + "\n__NEXTERM_PS_END__:0\n"
}

func TestFinderMatchesPortsProcessesAndRegex(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		query     FinderQuery
		wantKinds []string
		wantPIDs  []int
	}{
		{name: "port", query: FinderQuery{Kind: QueryPort, Value: "8080"}, wantKinds: []string{"listener", "listener"}, wantPIDs: []int{1201, 1202}},
		{name: "process substring", query: FinderQuery{Kind: QueryProcess, Value: "SSHD"}, wantKinds: []string{"process"}, wantPIDs: []int{812}},
		{name: "regex", query: FinderQuery{Kind: QueryRegex, Value: `envoy(\.yaml|$)`}, wantKinds: []string{"process"}, wantPIDs: []int{1440}},
	} {
		t.Run(test.name, func(t *testing.T) {
			matcher, err := newFinderMatcher(test.query)
			if err != nil {
				t.Fatal(err)
			}
			host := finderHostResult(execbatch.Result{Status: execbatch.StatusSuccess, Stdout: probeOutput("ss", ssSample, psSample)}, matcher)
			if host.ListenerError != "" || host.ProcessError != "" || len(host.Matches) != len(test.wantKinds) {
				t.Fatalf("finder host = %+v", host)
			}
			for index, match := range host.Matches {
				if match.Kind != test.wantKinds[index] {
					t.Errorf("match %d kind = %s", index, match.Kind)
				}
				pid := 0
				if match.Process != nil {
					pid = match.Process.PID
				} else if match.Listener != nil {
					pid = match.Listener.PID
				}
				if pid != test.wantPIDs[index] {
					t.Errorf("match %d pid = %d, want %d", index, pid, test.wantPIDs[index])
				}
			}
		})
	}
}

func TestFinderUsesNetstatAndReportsMissingTools(t *testing.T) {
	t.Parallel()
	matcher, err := newFinderMatcher(FinderQuery{Kind: QueryPort, Value: "443"})
	if err != nil {
		t.Fatal(err)
	}
	netstat := "tcp6 0 0 :::443 :::* LISTEN 1440/envoy"
	host := finderHostResult(execbatch.Result{Status: execbatch.StatusSuccess, Stdout: probeOutput("netstat", netstat, psSample)}, matcher)
	if host.ListenerTool != "netstat" || len(host.Matches) != 1 || host.Matches[0].Listener.Port != 443 {
		t.Fatalf("netstat fallback = %+v", host)
	}

	missing := "__NEXTERM_LISTENER_BEGIN__\n__NEXTERM_LISTENER_ERROR__missing ss and netstat\n__NEXTERM_LISTENER_END__:127\n" +
		"__NEXTERM_PS_BEGIN__\n__NEXTERM_PS_ERROR__missing ps\n__NEXTERM_PS_END__:127\n"
	host = finderHostResult(execbatch.Result{Status: execbatch.StatusSuccess, Stdout: missing}, matcher)
	if !strings.Contains(host.ListenerError, "缺少 ss 和 netstat") || !strings.Contains(host.ProcessError, "缺少 ps") || len(host.Matches) != 0 {
		t.Fatalf("missing tools = %+v", host)
	}

	sudoMissing := "__NEXTERM_LISTENER_BEGIN__\n__NEXTERM_TOOL__ss\nsudo is unavailable and no password can be requested\n__NEXTERM_LISTENER_END__:127\n" +
		"__NEXTERM_PS_BEGIN__\nsudo is unavailable and no password can be requested\n__NEXTERM_PS_END__:127\n"
	host = finderHostResult(execbatch.Result{Status: execbatch.StatusSuccess, Stdout: sudoMissing}, matcher)
	if !strings.Contains(host.ListenerError, "sudo is unavailable") || !strings.Contains(host.ProcessError, "sudo is unavailable") {
		t.Fatalf("sudo failure was not preserved in probe sections: %+v", host)
	}
}

func TestFinderRejectsInvalidQueriesBeforeExecution(t *testing.T) {
	t.Parallel()
	for _, query := range []FinderQuery{
		{Kind: QueryPort, Value: "0"}, {Kind: QueryPort, Value: "65536"}, {Kind: QueryPort, Value: "80;id"},
		{Kind: QueryRegex, Value: "["}, {Kind: "unknown", Value: "nginx"}, {Kind: QueryProcess, Value: "\n"},
	} {
		if _, err := newFinderMatcher(query); !errors.Is(err, ErrInvalid) {
			t.Errorf("query %+v error = %v", query, err)
		}
	}
}
