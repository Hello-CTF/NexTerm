package metrics

import (
	"runtime"
	"time"
)

// WriteProcess adds Go runtime, uptime and build metrics.
func WriteProcess(w *Writer, startedAt time.Time, version string) {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	w.Gauge("nexterm_build_info", "Build information; the value is always 1.", 1,
		L("version", version), L("goversion", runtime.Version()))
	w.Gauge("process_start_time_seconds", "Start time of the process since the Unix epoch in seconds.", float64(startedAt.UnixNano())/1e9)
	w.Gauge("nexterm_uptime_seconds", "Seconds since the server process started.", time.Since(startedAt).Seconds())
	w.Gauge("go_goroutines", "Number of goroutines that currently exist.", float64(runtime.NumGoroutine()))
	w.Gauge("go_memstats_heap_alloc_bytes", "Bytes of allocated heap objects.", float64(memory.HeapAlloc))
	w.Gauge("go_memstats_heap_sys_bytes", "Bytes of heap memory obtained from the OS.", float64(memory.HeapSys))
	w.Counter("go_gc_cycles_total", "Number of completed GC cycles.", float64(memory.NumGC))
}
