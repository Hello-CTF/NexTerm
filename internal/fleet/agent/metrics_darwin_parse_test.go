package agent

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

const vmStatAppleSilicon = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                    89092.
Pages active:                                 542625.
Pages inactive:                               479578.
Pages speculative:                             67769.
Pages throttled:                                   0.
Pages wired down:                             161657.
Pages purgeable:                               16100.
"Translation faults":                      315828174.
Pages copy-on-write:                        23562320.
Pages zero filled:                         177228591.
Pages reactivated:                           8019645.
Pages purged:                                2166907.
File-backed pages:                            442687.
Anonymous pages:                              647285.
Pages stored in compressor:                   442880.
Pages occupied by compressor:                 172149.
Decompressions:                              4088012.
Compressions:                                7430130.
Pageins:                                     7820638.
Pageouts:                                     207285.
Swapins:                                        1104.
Swapouts:                                      12604.
`

const vmStatIntel = `Mach Virtual Memory Statistics: (page size of 4096 bytes)
Pages free:                                 19321.
Pages active:                              926090.
Pages inactive:                            903719.
Pages speculative:                           23911.
Pages throttled:                                 0.
Pages wired down:                          1035979.
Pages purgeable:                             31091.
"Translation faults":                  1589054126.
Pages copy-on-write:                     20483970.
Pages zero filled:                      712361816.
Pages reactivated:                       8348411.
Pages purged:                             416011.
File-backed pages:                         869312.
Anonymous pages:                           984408.
Pages stored in compressor:                731031.
Pages occupied by compressor:              261740.
Decompressions:                         11104197.
Compressions:                           17020822.
Pageins:                                  6851216.
Pageouts:                                  218870.
Swapins:                                        0.
Swapouts:                                       0.
`

func TestParseVMStat(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		wantPages    int64
		wantPageSize int64
		wantErr      bool
	}{
		{
			name:         "apple silicon with compressor",
			output:       vmStatAppleSilicon,
			wantPages:    542625 + 161657 + 172149,
			wantPageSize: 16384,
		},
		{
			name:         "intel with compressor",
			output:       vmStatIntel,
			wantPages:    926090 + 1035979 + 261740,
			wantPageSize: 4096,
		},
		{
			name: "header without page size falls back",
			output: "Mach Virtual Memory Statistics:\n" +
				"Pages active:                                 100.\n" +
				"Pages wired down:                              20.\n" +
				"Pages occupied by compressor:                   3.\n",
			wantPages:    123,
			wantPageSize: 0,
		},
		{
			name: "no compressor counts active and wired only",
			output: "Mach Virtual Memory Statistics: (page size of 4096 bytes)\n" +
				"Pages free:                                     1000.\n" +
				"Pages active:                                   2000.\n" +
				"Pages wired down:                                300.\n",
			wantPages:    2300,
			wantPageSize: 4096,
		},
		{
			name: "missing active pages",
			output: "Mach Virtual Memory Statistics: (page size of 16384 bytes)\n" +
				"Pages wired down:                               161657.\n",
			wantErr: true,
		},
		{
			name: "missing wired pages",
			output: "Mach Virtual Memory Statistics: (page size of 16384 bytes)\n" +
				"Pages active:                                   542625.\n",
			wantErr: true,
		},
		{
			name: "unparsable active value",
			output: "Mach Virtual Memory Statistics: (page size of 16384 bytes)\n" +
				"Pages active:                                    lots.\n" +
				"Pages wired down:                               161657.\n",
			wantErr: true,
		},
		{
			name:    "empty output",
			output:  "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pages, pageSize, err := parseVMStat(tt.output)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseVMStat() = (%d, %d, nil), want error", pages, pageSize)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if pages != tt.wantPages || pageSize != tt.wantPageSize {
				t.Fatalf("parseVMStat() = (%d, %d), want (%d, %d)", pages, pageSize, tt.wantPages, tt.wantPageSize)
			}
		})
	}
}

const topTwoSamples = `Processes: 607 total, 3 running, 604 sleeping, 3256 threads 
2026/10/13 11:42:01
Load Avg: 2.11, 2.05, 1.98 
CPU usage: 1.37% user, 8.79% sys, 89.83% idle 
SharedLibs: 500M resident, 100M data, 50M linkedit.
MemRegions: 100000 total, 5000M resident, 200M private, 2000M shared.
PhysMem: 24G used (2000M wired, 5000M compressor), 500M unused.
VM: 300T vsize, 5000M framework vsize, 100000(0) swapins, 200000(0) swapouts.
Networks: packets: 1000000/500M in, 900000/400M out.
Disks: 500000/10G read, 300000/8G written.

Processes: 607 total, 3 running, 604 sleeping, 3256 threads 
2026/10/13 11:42:02
Load Avg: 2.11, 2.05, 1.98 
CPU usage: 0.72% user, 0.90% sys, 98.36% idle 
SharedLibs: 500M resident, 100M data, 50M linkedit.
MemRegions: 100000 total, 5000M resident, 200M private, 2000M shared.
PhysMem: 24G used (2000M wired, 5000M compressor), 500M unused.
VM: 300T vsize, 5000M framework vsize, 100000(0) swapins, 200000(0) swapouts.
Networks: packets: 1000000/500M in, 900000/400M out.
Disks: 500000/10G read, 300000/8G written.
`

func topSamples(usages ...string) string {
	var b strings.Builder
	for _, usage := range usages {
		fmt.Fprintf(&b, "Processes: 607 total, 3 running, 604 sleeping, 3256 threads \nLoad Avg: 2.11, 2.05, 1.98 \nCPU usage: %s \nPhysMem: 24G used, 500M unused.\n\n", usage)
	}
	return b.String()
}

func TestParseTopCPUPercent(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    float64
		wantErr bool
	}{
		{
			name:   "real two sample output uses second section",
			output: topTwoSamples,
			want:   100 - 98.36,
		},
		{
			name:   "fully busy clamps to 100",
			output: topSamples("1.37% user, 8.79% sys, 89.83% idle", "99.90% user, 0.10% sys, 0.00% idle"),
			want:   100,
		},
		{
			name:   "fully idle",
			output: topSamples("1.37% user, 8.79% sys, 89.83% idle", "0.00% user, 0.00% sys, 100.00% idle"),
			want:   0,
		},
		{
			name:   "idle above 100 clamps to 0",
			output: topSamples("1.37% user, 8.79% sys, 89.83% idle", "0.00% user, 0.00% sys, 100.40% idle"),
			want:   0,
		},
		{
			name:    "only one cpu usage section",
			output:  topSamples("1.37% user, 8.79% sys, 89.83% idle"),
			wantErr: true,
		},
		{
			name:    "no cpu usage section",
			output:  "Processes: 607 total\nPhysMem: 24G used.\n",
			wantErr: true,
		},
		{
			name:    "second section missing idle field",
			output:  topSamples("1.37% user, 8.79% sys, 89.83% idle", "10.00% user, 90.00% sys"),
			wantErr: true,
		},
		{
			name:    "second section unparsable idle",
			output:  topSamples("1.37% user, 8.79% sys, 89.83% idle", "10.00% user, lots% idle"),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTopCPUPercent(tt.output)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTopCPUPercent() = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("parseTopCPUPercent() = %v, want %v", got, tt.want)
			}
		})
	}
}
