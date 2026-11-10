package main

import (
	"strconv"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/metrics"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

var (
	terminalKinds = []string{session.KindLocal, session.KindSSH, session.KindDocker, session.KindWinRM, session.KindDevice}
	forwardKinds  = []string{forward.KindLocal, forward.KindSOCKS, forward.KindRemote}
	forwardStates = []string{forward.StatusActive, forward.StatusReconnecting, forward.StatusFailed}
)

// boundedLabel keeps a label inside its known value set so an unexpected
// value cannot grow the series count.
func boundedLabel(value string, known []string) string {
	for _, candidate := range known {
		if value == candidate {
			return value
		}
	}
	return "other"
}

func writeRuntimeMetrics(w *metrics.Writer, services *production.ProductionServices) {
	if services.Sessions != nil {
		counts := make(map[[2]string]int)
		for _, kind := range terminalKinds {
			counts[[2]string{kind, "false"}] = 0
			counts[[2]string{kind, "true"}] = 0
		}
		for _, entry := range services.Sessions.TabCounts() {
			counts[[2]string{boundedLabel(entry.Kind, terminalKinds), strconv.FormatBool(entry.Persistent)}] += entry.Count
		}
		writeCounts(w, counts, terminalKinds, []string{"false", "true"}, func(key [2]string, value int) {
			w.Gauge("nexterm_terminal_tabs", "Live terminal tabs by asset kind and whether a persistent terminal backs them.", float64(value),
				metrics.L("kind", key[0]), metrics.L("persistent", key[1]))
		})
	}
	if services.Forward != nil {
		counts := make(map[[2]string]int)
		for _, kind := range forwardKinds {
			for _, state := range forwardStates {
				counts[[2]string{kind, state}] = 0
			}
		}
		for _, spec := range services.Forward.List() {
			counts[[2]string{boundedLabel(spec.Kind, forwardKinds), boundedLabel(spec.Status, forwardStates)}]++
		}
		writeCounts(w, counts, forwardKinds, forwardStates, func(key [2]string, value int) {
			w.Gauge("nexterm_port_forwards", "Port forwards by kind and state.", float64(value),
				metrics.L("kind", key[0]), metrics.L("state", key[1]))
		})
	}
}

// writeCounts emits the known combinations in a stable order, then any
// "other" bucket that collected unexpected values.
func writeCounts(w *metrics.Writer, counts map[[2]string]int, first, second []string, emit func([2]string, int)) {
	for _, a := range append(append([]string(nil), first...), "other") {
		for _, b := range append(append([]string(nil), second...), "other") {
			key := [2]string{a, b}
			value, ok := counts[key]
			if !ok {
				continue
			}
			emit(key, value)
		}
	}
}
