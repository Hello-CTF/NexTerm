package hostmonitor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
)

// windowsOS is the Raw.OS and Snapshot.Platform of a host sampled through PowerShell.
const windowsOS = "Windows"

const windowsSentinel = sentinelPrefix + "windows"

// windowsHead and windowsTail make up the PowerShell sample script; it must stay Windows PowerShell 5.1 syntax.
// Every value is a raw CIM integer so that no locale-dependent number or date formatting reaches the output:
// the processor and network counters are cumulative and turn into rates between samples like /proc counters do,
// and the uptime comes from the System object's boot time in its own timebase. Each query fails on its own, so a
// blocked class leaves its metric out and names the cause in errors.
const windowsHead = `$ErrorActionPreference='Stop'
try{[Console]::OutputEncoding=[Text.Encoding]::UTF8}catch{}
Write-Output '` + windowsSentinel + `'
$r=@{};$e=@()
try{$o=Get-CimInstance Win32_OperatingSystem;$r.os=@{mem=$o.TotalVisibleMemorySize;free=$o.FreePhysicalMemory;swap=$o.SizeStoredInPagingFiles;swapFree=$o.FreeSpaceInPagingFiles;drive=$o.SystemDrive}}catch{$e+='os: '+$_}
try{$s=Get-CimInstance Win32_PerfRawData_PerfOS_System;$r.up=@{boot=$s.SystemUpTime;ts=$s.Timestamp_Object;freq=$s.Frequency_Object}}catch{$e+='uptime: '+$_}
try{$p=@(Get-CimInstance Win32_PerfRawData_PerfOS_Processor);$c=@($p|Where-Object{$_.Name -eq '_Total'});if($c.Count){$r.cpu=@{idle=$c[0].PercentProcessorTime;ts=$c[0].Timestamp_Sys100NS;n=$p.Count-1}}}catch{$e+='cpu: '+$_}
try{$r.net=@(Get-CimInstance Win32_PerfRawData_Tcpip_NetworkInterface|ForEach-Object{@{name=$_.Name;rx=$_.BytesReceivedPersec;tx=$_.BytesSentPersec}})}catch{$e+='net: '+$_}
`

// windowsDisks lists fixed drives only; network and removable drives can block the query.
const windowsDisks = `try{$r.disks=@(Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3'|ForEach-Object{@{id=$_.DeviceID;fs=$_.FileSystem;size=$_.Size;free=$_.FreeSpace}})}catch{$e+='disks: '+$_}
`

const windowsTail = `$r.errors=$e
$r|ConvertTo-Json -Compress -Depth 4`

// WindowsSampleCommand returns the command that collects one sample on a Windows host; withDisks false leaves the
// drives out. It runs through powershell -EncodedCommand, which cmd.exe, PowerShell and a WinRM shell all accept
// without quoting.
func WindowsSampleCommand(withDisks bool) string {
	script := windowsHead
	if withDisks {
		script += windowsDisks
	}
	return psquote.ShortCommand(script + windowsTail)
}

// windowsOSName reports a uname -s that a POSIX layer on Windows (Git for Windows, MSYS2, Cygwin) prints; such a
// host has no /proc worth reading and is sampled through PowerShell instead.
func windowsOSName(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range []string{"MINGW", "MSYS", "CYGWIN", "WINDOWS"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

type windowsOSSection struct {
	Mem      *uint64 `json:"mem"`
	Free     *uint64 `json:"free"`
	Swap     *uint64 `json:"swap"`
	SwapFree *uint64 `json:"swapFree"`
	Drive    string  `json:"drive"`
}

type windowsUptimeSection struct {
	Boot *uint64 `json:"boot"`
	TS   *uint64 `json:"ts"`
	Freq *uint64 `json:"freq"`
}

type windowsCPUSection struct {
	Idle *uint64 `json:"idle"`
	TS   *uint64 `json:"ts"`
	N    *int    `json:"n"`
}

type windowsNetEntry struct {
	Name string  `json:"name"`
	RX   *uint64 `json:"rx"`
	TX   *uint64 `json:"tx"`
}

type windowsDiskEntry struct {
	ID   string  `json:"id"`
	FS   string  `json:"fs"`
	Size *uint64 `json:"size"`
	Free *uint64 `json:"free"`
}

// ParseWindowsOutput parses the output of WindowsSampleCommand. Output without the sentinel did not come from the
// script (no PowerShell on the host) and yields ErrNoSentinel. A section that is missing or malformed leaves its
// metric unset; when none is left the error wraps ErrNoMetrics and carries the failures the script reported.
func ParseWindowsOutput(stdout string) (Raw, error) {
	text := strings.ReplaceAll(stdout, "\r", "")
	index := strings.Index(text, windowsSentinel)
	if index < 0 {
		return Raw{}, ErrNoSentinel
	}
	raw := Raw{OS: windowsOS}
	rest := text[index+len(windowsSentinel):]
	if newline := strings.IndexByte(rest, '\n'); newline >= 0 {
		rest = rest[newline+1:]
	} else {
		rest = ""
	}
	var sections map[string]json.RawMessage
	if err := json.NewDecoder(strings.NewReader(rest)).Decode(&sections); err != nil || sections == nil {
		return raw, fmt.Errorf("%w: 输出不是有效的 JSON", ErrNoMetrics)
	}
	var reasons []string
	if list, ok := decodeList[string](sections["errors"]); ok {
		for _, reason := range list {
			if reason = strings.TrimSpace(reason); reason != "" {
				reasons = append(reasons, reason)
			}
		}
	}
	systemDrive := ""
	if info, ok := decodeSection[windowsOSSection](sections["os"]); ok {
		raw.Memory = windowsMemory(info)
		systemDrive = info.Drive
	}
	if up, ok := decodeSection[windowsUptimeSection](sections["up"]); ok {
		raw.Uptime = windowsUptime(up)
	}
	if cpu, ok := decodeSection[windowsCPUSection](sections["cpu"]); ok && cpu.Idle != nil && cpu.TS != nil && *cpu.TS > 0 {
		raw.CPU = &cpuTimes{total: *cpu.TS, idle: *cpu.Idle}
		if cpu.N != nil && *cpu.N > 0 {
			raw.CPUCount = *cpu.N
		}
	}
	if list, ok := decodeList[windowsNetEntry](sections["net"]); ok {
		raw.Net = windowsNet(list)
	}
	if list, ok := decodeList[windowsDiskEntry](sections["disks"]); ok {
		raw.Disks = windowsDisksOf(list, systemDrive)
	}
	if raw.CPU == nil && raw.Memory == nil && len(raw.Net) == 0 && raw.Uptime == nil && len(raw.Disks) == 0 {
		if len(reasons) == 0 {
			return raw, ErrNoMetrics
		}
		return raw, fmt.Errorf("%w: %s", ErrNoMetrics, strings.Join(reasons, "; "))
	}
	return raw, nil
}

func decodeSection[T any](data json.RawMessage) (T, bool) {
	var value T
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return value, false
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, false
	}
	return value, true
}

// decodeList accepts an array or a single element; an element that does not decode is skipped. Windows PowerShell
// 5.1 serializes an array that carries extended type data as {"value":[...],"Count":n}, which is unwrapped.
func decodeList[T any](data json.RawMessage) ([]T, bool) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, false
	}
	if data[0] == '{' {
		var wrapped struct {
			Value json.RawMessage `json:"value"`
		}
		if json.Unmarshal(data, &wrapped) == nil && bytes.HasPrefix(bytes.TrimSpace(wrapped.Value), []byte("[")) {
			data = bytes.TrimSpace(wrapped.Value)
		}
	}
	if data[0] != '[' {
		value, ok := decodeSection[T](data)
		if !ok {
			return nil, false
		}
		return []T{value}, true
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, false
	}
	out := make([]T, 0, len(items))
	for _, item := range items {
		if value, ok := decodeSection[T](item); ok {
			out = append(out, value)
		}
	}
	return out, true
}

// windowsMemory reads Win32_OperatingSystem, which reports KiB. FreePhysicalMemory is what Windows calls available
// memory (free, zeroed and standby pages); the paging file sizes stand in for swap.
func windowsMemory(info windowsOSSection) *Memory {
	if info.Mem == nil || *info.Mem == 0 || info.Free == nil {
		return nil
	}
	total := *info.Mem * 1024
	available := min(*info.Free*1024, total)
	memory := &Memory{TotalBytes: total, AvailableBytes: available, UsedBytes: total - available}
	if info.Swap != nil {
		swapTotal := *info.Swap * 1024
		swapFree := uint64(0)
		if info.SwapFree != nil {
			swapFree = *info.SwapFree * 1024
		}
		memory.SwapTotalBytes = swapTotal
		memory.SwapUsedBytes = swapTotal - min(swapFree, swapTotal)
	}
	return memory
}

// windowsUptime derives the uptime from the System object, whose SystemUpTime is the boot time in the
// Timestamp_Object timebase that ticks Frequency_Object times per second.
func windowsUptime(up windowsUptimeSection) *float64 {
	if up.Boot == nil || up.TS == nil || up.Freq == nil || *up.Freq == 0 || *up.TS < *up.Boot {
		return nil
	}
	value := float64(*up.TS-*up.Boot) / float64(*up.Freq)
	return &value
}

// windowsNet keeps the adapters with both counters. The tunnel pseudo-interfaces carry no host traffic of their
// own and are left out, as loopback is on Linux.
func windowsNet(entries []windowsNetEntry) []netCounters {
	out := []netCounters{}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" || entry.RX == nil || entry.TX == nil || seen[name] || windowsTunnel(name) {
			continue
		}
		seen[name] = true
		out = append(out, netCounters{name: name, rx: *entry.RX, tx: *entry.TX})
	}
	return out
}

func windowsTunnel(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range []string{"isatap", "teredo", "6to4", "loopback", "pseudo-interface"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// windowsDisksOf orders the fixed drives like parseDF orders file systems: the system drive first, then the
// others from the fullest down, at most maxDisks of them.
func windowsDisksOf(entries []windowsDiskEntry, systemDrive string) []Disk {
	var root *Disk
	var others []Disk
	seen := map[string]bool{}
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" || entry.Size == nil || *entry.Size == 0 || entry.Free == nil || seen[strings.ToUpper(id)] {
			continue
		}
		seen[strings.ToUpper(id)] = true
		total := *entry.Size
		available := min(*entry.Free, total)
		disk := Disk{
			Mount: id, Filesystem: strings.TrimSpace(entry.FS),
			TotalBytes: total, UsedBytes: total - available, AvailableBytes: available,
			UsedPercent: float64(total-available) / float64(total) * 100,
		}
		if root == nil && systemDrive != "" && strings.EqualFold(id, strings.TrimSpace(systemDrive)) {
			root = &disk
			continue
		}
		others = append(others, disk)
	}
	slices.SortStableFunc(others, func(a, b Disk) int {
		switch {
		case a.UsedPercent > b.UsedPercent:
			return -1
		case a.UsedPercent < b.UsedPercent:
			return 1
		}
		return strings.Compare(a.Mount, b.Mount)
	})
	out := []Disk{}
	if root != nil {
		out = append(out, *root)
	}
	out = append(out, others...)
	if len(out) > maxDisks {
		out = out[:maxDisks]
	}
	return out
}

// windowsFailure is the reason shown for Windows output that carried no metric.
func windowsFailure(err error) string {
	detail, ok := strings.CutPrefix(err.Error(), ErrNoMetrics.Error()+": ")
	if !ok || !errors.Is(err, ErrNoMetrics) {
		return "无法读取 Windows 主机指标"
	}
	const maxDetail = 300
	if len(detail) > maxDetail {
		detail = strings.ToValidUTF8(detail[:maxDetail], "") + "…"
	}
	return fmt.Sprintf("无法读取 Windows 主机指标：%s", detail)
}
