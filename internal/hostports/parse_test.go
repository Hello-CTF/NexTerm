package hostports

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The fixtures under testdata are constructed from the documented column layouts of each tool
// (iproute2 ss, net-tools netstat, BSD netstat, lsof, Windows netstat/tasklist). They were not
// recorded from real machines; replace them with recordings during hardware acceptance.

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func describe(listeners []Listener) []string {
	out := make([]string, 0, len(listeners))
	for _, l := range listeners {
		out = append(out, fmt.Sprintf("%s %s %d pid=%d %s", l.Proto, l.Address, l.Port, l.PID, l.Process))
	}
	sort.Strings(out)
	return out
}

func TestParseFixtures(t *testing.T) {
	cases := []struct {
		name   string
		format Format
		file   string
		want   []string
	}{
		{"ss", FormatSS, "ss_ltnH.txt", []string{
			"tcp 0.0.0.0 22 pid=0 ", "tcp 0.0.0.0 80 pid=0 ", "tcp 127.0.0.53 53 pid=0 ", "tcp 127.0.0.1 631 pid=0 ",
			"tcp6 :: 22 pid=0 ", "tcp6 ::1 5432 pid=0 ", "tcp6 ::ffff:127.0.0.1 8080 pid=0 ", "tcp6 fe80::1%eth0 9000 pid=0 ",
		}},
		{"ss with processes", FormatSS, "ss_ltnpH.txt", []string{
			"tcp 0.0.0.0 22 pid=812 sshd", "tcp 0.0.0.0 80 pid=901 nginx", "tcp 127.0.0.53 53 pid=0 ",
			"tcp6 :: 22 pid=812 sshd", "tcp6 ::1 5432 pid=77 postgres", "tcp6 ::ffff:127.0.0.1 8080 pid=123 node",
		}},
		{"linux netstat", FormatNetstatLinux, "netstat_linux_ltn.txt", []string{
			"tcp 0.0.0.0 22 pid=0 ", "tcp 127.0.0.53 53 pid=0 ", "tcp 127.0.0.1 631 pid=0 ",
			"tcp6 :: 22 pid=0 ", "tcp6 ::1 5432 pid=0 ", "tcp6 :: 80 pid=0 ",
		}},
		{"linux netstat with processes", FormatNetstatLinux, "netstat_linux_ltnp.txt", []string{
			"tcp 127.0.0.53 53 pid=0 ", "tcp 0.0.0.0 22 pid=812 sshd", "tcp 0.0.0.0 443 pid=901 nginx: master p",
			"tcp6 :: 22 pid=812 sshd", "tcp6 ::1 5432 pid=77 postgres",
		}},
		{"macos netstat -anv", FormatNetstatBSD, "netstat_macos_anv.txt", []string{
			"tcp 127.0.0.1 5432 pid=412 ", "tcp6 :: 3000 pid=980 ", "tcp6 :: 8080 pid=981 ", "tcp6 ::1 8081 pid=982 ",
			"tcp 0.0.0.0 5000 pid=200 ", "tcp6 fe80::1%lo0 8082 pid=983 ",
		}},
		{"macos netstat -an", FormatNetstatBSD, "netstat_macos_an.txt", []string{
			"tcp 127.0.0.1 5432 pid=0 ", "tcp6 :: 3000 pid=0 ", "tcp6 ::1 8081 pid=0 ",
		}},
		{"lsof", FormatLsof, "lsof_macos.txt", []string{
			"tcp 0.0.0.0 49152 pid=412 rapportd", "tcp6 :: 49152 pid=412 rapportd", "tcp6 ::1 8080 pid=980 node",
			"tcp 127.0.0.1 5432 pid=55 postgres", "tcp 127.0.0.1 9222 pid=77 Google Chrome",
		}},
		{"windows english", FormatNetstatWindows, "netstat_windows_en.txt", []string{
			"tcp 0.0.0.0 135 pid=1012 ", "tcp 0.0.0.0 445 pid=4 ", "tcp 127.0.0.1 5432 pid=4120 ", "tcp 192.168.1.7 139 pid=4 ",
			"tcp6 :: 135 pid=1012 ", "tcp6 ::1 8080 pid=5544 ", "tcp6 fe80::1%12 9000 pid=5544 ",
		}},
		{"windows chinese", FormatNetstatWindows, "netstat_windows_zh.txt", []string{
			"tcp 0.0.0.0 135 pid=1012 ", "tcp 0.0.0.0 3389 pid=1100 ", "tcp 127.0.0.1 5432 pid=4120 ", "tcp6 :: 445 pid=4 ",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.format, fixture(t, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if gotText := describe(got); fmt.Sprint(gotText) != fmt.Sprint(want) {
				t.Fatalf("got\n%v\nwant\n%v", gotText, want)
			}
			for i := 1; i < len(got); i++ {
				if got[i-1].Port > got[i].Port {
					t.Fatalf("not sorted by port: %v", got)
				}
			}
		})
	}
}

func TestParseTasklistCSV(t *testing.T) {
	names := ParseTasklistCSV(fixture(t, "tasklist_csv.txt"))
	if names[4120] != "postgres.exe" || names[5544] != "node.exe" || names[0] != "System Idle Process" {
		t.Fatalf("names = %v", names)
	}
	if len(ParseTasklistCSV("not csv \"")) != 0 {
		t.Fatal("garbage produced names")
	}
	listeners, err := Parse(FormatNetstatWindows, fixture(t, "netstat_windows_en.txt"))
	if err != nil {
		t.Fatal(err)
	}
	AttachProcessNames(listeners, names)
	found := false
	for _, l := range listeners {
		if l.PID == 4120 {
			found = l.Process == "postgres.exe"
		}
	}
	if !found {
		t.Fatalf("process name not attached: %v", listeners)
	}
}

func TestParseEmptyAndGarbage(t *testing.T) {
	for _, format := range []Format{FormatSS, FormatNetstatLinux, FormatNetstatBSD, FormatLsof, FormatNetstatWindows} {
		if got, err := Parse(format, ""); err != nil || len(got) != 0 {
			t.Fatalf("%s empty: %v %v", format, got, err)
		}
		garbage := "\x00\xff\xfe binary \x1b[31m soup\n::::\n[[[]]] 999999999999\n\tLISTEN\n"
		if _, err := Parse(format, garbage); err == nil {
			t.Fatalf("%s: garbage did not error", format)
		}
	}
	if _, err := Parse(Format("nope"), "x"); err == nil {
		t.Fatal("unknown format accepted")
	}
	// Unrecognized lines mixed into valid output are skipped.
	got, err := Parse(FormatSS, "weird line\nLISTEN 0 128 0.0.0.0:22 0.0.0.0:*\nLISTEN 0 1 bad:port x:*\n")
	if err != nil || len(got) != 1 || got[0].Port != 22 {
		t.Fatalf("got %v err %v", got, err)
	}
}

func TestNormalizeDeduplicates(t *testing.T) {
	got := Normalize([]Listener{
		{Proto: "tcp", Address: "0.0.0.0", Port: 22},
		{Proto: "tcp", Address: "0.0.0.0", Port: 22, PID: 5, Process: "sshd"},
		{Proto: "tcp6", Address: "::", Port: 22},
		{Proto: "tcp", Address: "127.0.0.1", Port: 8},
	})
	if len(got) != 3 || got[0].Port != 8 || got[1].PID != 5 {
		t.Fatalf("got %+v", got)
	}
}
