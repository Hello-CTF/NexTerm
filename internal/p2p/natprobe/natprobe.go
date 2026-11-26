// Package natprobe classifies the local NAT's mapping behaviour (design 2.4)
// by sending STUN Binding requests to several STUN addresses from one UDP
// socket and comparing the mapped addresses they report.
//
// Limitation: when every STUN address shares one IP and differs only by port,
// the probe detects port-dependent mapping but not address-only-dependent
// mapping. Result.DistinctIPs says whether the comparison covered two IPs.
package natprobe

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/pion/stun/v4"
)

// Mapping is the NAT mapping classification carried in offer/answer.nat.
type Mapping string

const (
	// Independent: every STUN server saw the same mapped address, so a
	// single srflx candidate is reachable from any peer (cone-like).
	Independent Mapping = "independent"
	// Dependent: servers saw different mapped addresses (symmetric/hard).
	Dependent Mapping = "dependent"
	// Blocked: no STUN server answered; UDP or STUN is unreachable.
	Blocked Mapping = "blocked"
	// Unknown: fewer than two answers to compare.
	Unknown Mapping = "unknown"
)

// Defaults for Options.
const (
	DefaultTimeout    = 2 * time.Second
	DefaultRetransmit = 250 * time.Millisecond
)

// Options tunes a probe. Zero values select the defaults.
type Options struct {
	// Timeout bounds the whole probe.
	Timeout time.Duration
	// Retransmit is the interval between repeated requests to a server that
	// has not answered yet.
	Retransmit time.Duration
	// Network is "udp4" (default) or "udp6".
	Network string
	// ListenPacket opens the probe socket; nil uses net.ListenPacket. Tests
	// pass a virtual network here.
	ListenPacket func(network, address string) (net.PacketConn, error)
	// ResolveUDPAddr resolves a host:port; nil uses net.ResolveUDPAddr.
	ResolveUDPAddr func(network, address string) (*net.UDPAddr, error)
}

// Observation is one STUN server's answer.
type Observation struct {
	Server string         // as configured
	Addr   netip.AddrPort // resolved server address
	Mapped netip.AddrPort // zero if the server did not answer
}

// Result is the probe outcome.
type Result struct {
	Mapping      Mapping
	Observations []Observation
	// DistinctIPs is true when answers came from at least two server IPs,
	// i.e. address-dependent mapping could also be detected.
	DistinctIPs bool
}

// Probe runs the classification. It returns an error only for unusable
// input or a socket that cannot be opened; an unreachable network is a
// Blocked result, not an error.
func Probe(ctx context.Context, servers []string, opts Options) (Result, error) {
	opts = opts.withDefaults()
	targets, err := resolveTargets(servers, opts)
	if err != nil {
		return Result{Mapping: Unknown}, err
	}
	if len(targets) == 0 {
		return Result{Mapping: Unknown}, nil
	}
	laddr := "0.0.0.0:0"
	if opts.Network == "udp6" {
		laddr = "[::]:0"
	}
	pc, err := opts.ListenPacket(opts.Network, laddr)
	if err != nil {
		return Result{Mapping: Unknown}, err
	}
	defer func() { _ = pc.Close() }()

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	obs := exchange(ctx, pc, targets, opts.Retransmit)
	return classify(obs), nil
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Retransmit <= 0 {
		o.Retransmit = DefaultRetransmit
	}
	if o.Network == "" {
		o.Network = "udp4"
	}
	if o.ListenPacket == nil {
		o.ListenPacket = net.ListenPacket
	}
	if o.ResolveUDPAddr == nil {
		o.ResolveUDPAddr = net.ResolveUDPAddr
	}
	return o
}

type target struct {
	server string
	addr   netip.AddrPort
	txID   [stun.TransactionIDSize]byte
}

// resolveTargets resolves each server and drops duplicates of one address,
// which would otherwise always look like an independent mapping.
func resolveTargets(servers []string, opts Options) ([]target, error) {
	seen := make(map[netip.AddrPort]bool)
	var out []target
	var firstErr error
	for _, s := range servers {
		ua, err := opts.ResolveUDPAddr(opts.Network, s)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ap := ua.AddrPort()
		ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
		if !ap.IsValid() || seen[ap] {
			continue
		}
		seen[ap] = true
		out = append(out, target{server: s, addr: ap, txID: stun.NewTransactionID()})
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func exchange(ctx context.Context, pc net.PacketConn, targets []target, retransmit time.Duration) []Observation {
	obs := make([]Observation, len(targets))
	byTx := make(map[[stun.TransactionIDSize]byte]int, len(targets))
	for i, t := range targets {
		obs[i] = Observation{Server: t.server, Addr: t.addr}
		byTx[t.txID] = i
	}

	var mu sync.Mutex
	answered := 0
	allDone := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 1500)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			mapped, tx, ok := parseResponse(buf[:n])
			if !ok {
				continue
			}
			mu.Lock()
			i, known := byTx[tx]
			if known && !obs[i].Mapped.IsValid() {
				obs[i].Mapped = mapped
				answered++
				if answered == len(targets) {
					close(allDone)
				}
			}
			mu.Unlock()
		}
	}()

	tick := time.NewTicker(retransmit)
	defer tick.Stop()
send:
	for {
		mu.Lock()
		for i, t := range targets {
			if !obs[i].Mapped.IsValid() {
				req, err := stun.Build(stun.BindingRequest, stun.NewTransactionIDSetter(t.txID), stun.Fingerprint)
				if err == nil {
					_, _ = pc.WriteTo(req.Raw, net.UDPAddrFromAddrPort(t.addr))
				}
			}
		}
		mu.Unlock()
		select {
		case <-allDone:
			break send
		case <-ctx.Done():
			break send
		case <-tick.C:
		}
	}
	// Unblock the reader, then wait so obs is no longer written.
	_ = pc.SetReadDeadline(time.Now())
	_ = pc.Close()
	<-readDone
	return obs
}

func parseResponse(b []byte) (netip.AddrPort, [stun.TransactionIDSize]byte, bool) {
	var zero [stun.TransactionIDSize]byte
	if !stun.IsMessage(b) {
		return netip.AddrPort{}, zero, false
	}
	m := &stun.Message{Raw: append([]byte(nil), b...)}
	if err := m.Decode(); err != nil || m.Type != stun.BindingSuccess {
		return netip.AddrPort{}, zero, false
	}
	var ip net.IP
	var port int
	var xor stun.XORMappedAddress
	if err := xor.GetFrom(m); err == nil {
		ip, port = xor.IP, xor.Port
	} else {
		var plain stun.MappedAddress
		if err := plain.GetFrom(m); err != nil {
			return netip.AddrPort{}, zero, false
		}
		ip, port = plain.IP, plain.Port
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok || port <= 0 || port > 65535 {
		return netip.AddrPort{}, zero, false
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), m.TransactionID, true
}

func classify(obs []Observation) Result {
	r := Result{Observations: obs}
	var answers []Observation
	for _, o := range obs {
		if o.Mapped.IsValid() {
			answers = append(answers, o)
		}
	}
	switch {
	case len(answers) == 0:
		r.Mapping = Blocked
		return r
	case len(answers) < 2:
		r.Mapping = Unknown
		return r
	}
	r.Mapping = Independent
	for _, o := range answers[1:] {
		if o.Mapped != answers[0].Mapped {
			r.Mapping = Dependent
		}
		if o.Addr.Addr() != answers[0].Addr.Addr() {
			r.DistinctIPs = true
		}
	}
	return r
}

// HostOnly reports whether design 2.4 says to skip hole punching for this
// pair of classifications: both dependent, or either blocked.
func HostOnly(local, remote Mapping) bool {
	return (local == Dependent && remote == Dependent) || local == Blocked || remote == Blocked
}
