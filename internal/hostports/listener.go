// Package hostports lists the TCP listening ports of a managed host and runs
// bounded connectivity probes through an existing session's exec channel.
package hostports

import "sort"

// Listener is one listening TCP socket on the host.
type Listener struct {
	// Proto is "tcp" for IPv4 sockets and "tcp6" for IPv6 sockets.
	Proto string `json:"proto"`
	// Address is the bound address without brackets; wildcards are "0.0.0.0" or "::".
	Address string `json:"address"`
	Port    uint16 `json:"port"`
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
}

// Normalize removes duplicate (proto, address, port) entries, keeping the entry that carries
// process information, and sorts by port, then address, then protocol.
func Normalize(listeners []Listener) []Listener {
	type key struct {
		proto, address string
		port           uint16
	}
	index := make(map[key]int, len(listeners))
	out := make([]Listener, 0, len(listeners))
	for _, l := range listeners {
		k := key{l.Proto, l.Address, l.Port}
		if i, ok := index[k]; ok {
			if out[i].PID == 0 && l.PID != 0 {
				out[i] = l
			} else if out[i].Process == "" && l.Process != "" && (l.PID == out[i].PID || l.PID == 0) {
				out[i].Process = l.Process
			}
			continue
		}
		index[k] = len(out)
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		if out[i].Address != out[j].Address {
			return out[i].Address < out[j].Address
		}
		return out[i].Proto < out[j].Proto
	})
	return out
}

// AttachProcessNames fills Process for listeners whose PID appears in names and that have no name yet.
func AttachProcessNames(listeners []Listener, names map[int]string) {
	for i := range listeners {
		if listeners[i].Process == "" && listeners[i].PID != 0 {
			listeners[i].Process = names[listeners[i].PID]
		}
	}
}
