package hostmonitor

import "strings"

const sentinelPrefix = "@@nexterm-monitor "

// Section names written by SampleCommand, in output order.
const (
	sectionOS      = "os"
	sectionStat    = "stat"
	sectionMeminfo = "meminfo"
	sectionLoadavg = "loadavg"
	sectionNetDev  = "netdev"
	sectionUptime  = "uptime"
	sectionDF      = "df"
	// sectionDFTimeout follows the df section when df was killed for running too long.
	sectionDFTimeout = "df-timeout"
	sectionEnd       = "end"
)

// dfTimeoutSeconds bounds df on the host; tests shorten it. A hung mount (FUSE, a dying block device) would
// otherwise leave df, and the SSH session channel it holds, running on the host after the client gave up.
var dfTimeoutSeconds = "5"

// sampleHead reads the metrics that come from /proc, in a single round trip. Only the cpu lines of /proc/stat are
// read because the rest of the file (interrupt counters) grows with the core count.
const sampleHead = `LC_ALL=C; export LC_ALL
s() { printf '` + sentinelPrefix + `%s\n' "$*"; }
os=$(uname -s 2>/dev/null)
s ` + sectionOS + ` "$os"
if [ "$os" = Linux ]; then
s ` + sectionStat + `; grep '^cpu' /proc/stat 2>/dev/null
s ` + sectionMeminfo + `; cat /proc/meminfo 2>/dev/null
s ` + sectionLoadavg + `; cat /proc/loadavg 2>/dev/null
s ` + sectionNetDev + `; cat /proc/net/dev 2>/dev/null
s ` + sectionUptime + `; cat /proc/uptime 2>/dev/null
`

// sampleDisks lists the file systems. df -l keeps network mounts, which can hang, out of the listing; a df without
// -l (busybox) falls back to the root file system alone. df runs under timeout when the host has a working one;
// exit codes 124 (GNU), 137 (killed) and 143 (busybox) mean it was stopped.
func sampleDisks() string {
	return `t=
if command -v timeout >/dev/null 2>&1 && timeout -s KILL ` + dfTimeoutSeconds + ` true 2>/dev/null; then t="timeout -s KILL ` + dfTimeoutSeconds + `"; fi
s ` + sectionDF + `
$t df -P -l 2>/dev/null; rc=$?
if [ "$rc" != 0 ] && [ "$rc" != 124 ] && [ "$rc" != 137 ] && [ "$rc" != 143 ]; then $t df -P / 2>/dev/null; rc=$?; fi
case "$rc" in 124|137|143) s ` + sectionDFTimeout + `;; esac
`
}

const sampleTail = `fi
s ` + sectionEnd

// SampleCommand returns the command that collects one sample; withDisks false leaves df out. The script runs under
// sh so that a login shell such as fish or cmd.exe never has to understand it; a host without sh prints no sentinel
// and is reported as unsupported.
func SampleCommand(withDisks bool) string {
	script := sampleHead
	if withDisks {
		script += sampleDisks()
	}
	return "sh -c " + shellQuote(script+sampleTail)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
