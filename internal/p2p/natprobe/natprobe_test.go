package natprobe_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe"
	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe/stuntest"
	"github.com/pion/transport/v5/vnet"
)

var (
	coneNAT      = &vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointIndependent}
	addrDepNAT   = &vnet.NATType{MappingBehavior: vnet.EndpointAddrDependent, FilteringBehavior: vnet.EndpointAddrDependent}
	symmetricNAT = &vnet.NATType{MappingBehavior: vnet.EndpointAddrPortDependent, FilteringBehavior: vnet.EndpointAddrPortDependent}
)

func topology(t *testing.T, stunIPs []string, ports []int, nat *vnet.NATType) (*stuntest.Topology, *vnet.Net) {
	t.Helper()
	site := stuntest.Site{NAT: nat, PublicIP: "27.1.1.1", CIDR: "192.168.0.0/24", Hosts: []string{"192.168.0.2"}}
	if nat == nil {
		site = stuntest.Site{Hosts: []string{"27.1.1.1"}}
	}
	topo, err := stuntest.NewTopology(stunIPs, ports, site)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = topo.Close() })
	return topo, topo.Hosts[0][0]
}

func probe(t *testing.T, host *vnet.Net, servers []string, timeout time.Duration) natprobe.Result {
	t.Helper()
	res, err := natprobe.Probe(context.Background(), servers, natprobe.Options{
		Timeout:        timeout,
		Retransmit:     50 * time.Millisecond,
		ListenPacket:   host.ListenPacket,
		ResolveUDPAddr: host.ResolveUDPAddr,
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return res
}

func TestProbeClassifiesMapping(t *testing.T) {
	cases := []struct {
		name        string
		nat         *vnet.NATType
		ips         []string
		ports       []int
		want        natprobe.Mapping
		distinctIPs bool
	}{
		{"no NAT", nil, []string{"1.2.3.4", "5.6.7.8"}, []int{3478}, natprobe.Independent, true},
		{"cone two IPs", coneNAT, []string{"1.2.3.4", "5.6.7.8"}, []int{3478}, natprobe.Independent, true},
		{"cone one IP two ports", coneNAT, []string{"1.2.3.4"}, []int{3478, 5349}, natprobe.Independent, false},
		{"symmetric two IPs", symmetricNAT, []string{"1.2.3.4", "5.6.7.8"}, []int{3478}, natprobe.Dependent, true},
		{"symmetric one IP two ports", symmetricNAT, []string{"1.2.3.4"}, []int{3478, 5349}, natprobe.Dependent, false},
		{"address-dependent two IPs", addrDepNAT, []string{"1.2.3.4", "5.6.7.8"}, []int{3478}, natprobe.Dependent, true},
		// The documented blind spot: one IP cannot reveal address-only dependence.
		{"address-dependent one IP two ports", addrDepNAT, []string{"1.2.3.4"}, []int{3478, 5349}, natprobe.Independent, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo, host := topology(t, tc.ips, tc.ports, tc.nat)
			res := probe(t, host, topo.STUN, 3*time.Second)
			if res.Mapping != tc.want {
				t.Fatalf("Mapping = %q, want %q (observations %+v)", res.Mapping, tc.want, res.Observations)
			}
			if res.DistinctIPs != tc.distinctIPs {
				t.Fatalf("DistinctIPs = %v, want %v", res.DistinctIPs, tc.distinctIPs)
			}
			for _, o := range res.Observations {
				if o.Mapped.Addr().String() != "27.1.1.1" {
					t.Fatalf("mapped address %v, want the public IP", o.Mapped)
				}
			}
		})
	}
}

func TestProbeBlockedAndUnknown(t *testing.T) {
	topo, host := topology(t, []string{"1.2.3.4", "5.6.7.8"}, []int{3478}, coneNAT)

	topo.Server(0).SetSilent(true)
	topo.Server(1).SetSilent(true)
	if res := probe(t, host, topo.STUN, 300*time.Millisecond); res.Mapping != natprobe.Blocked {
		t.Fatalf("all silent: Mapping = %q, want blocked", res.Mapping)
	}
	if topo.Server(0).Requests() < 2 {
		t.Fatalf("expected retransmitted requests, got %d", topo.Server(0).Requests())
	}

	topo.Server(1).SetSilent(false)
	res := probe(t, host, topo.STUN, 300*time.Millisecond)
	if res.Mapping != natprobe.Unknown {
		t.Fatalf("one answer: Mapping = %q, want unknown", res.Mapping)
	}
	if res.Observations[0].Mapped.IsValid() || !res.Observations[1].Mapped.IsValid() {
		t.Fatalf("observations = %+v", res.Observations)
	}

	if res := probe(t, host, topo.STUN[1:], time.Second); res.Mapping != natprobe.Unknown {
		t.Fatalf("single server: Mapping = %q, want unknown", res.Mapping)
	}
	if res := probe(t, host, nil, time.Second); res.Mapping != natprobe.Unknown {
		t.Fatalf("no servers: Mapping = %q, want unknown", res.Mapping)
	}
	// Two names for one address are one comparison point, not two.
	if res := probe(t, host, []string{topo.STUN[1], topo.STUN[1]}, time.Second); res.Mapping != natprobe.Unknown || len(res.Observations) != 1 {
		t.Fatalf("duplicate server: %+v", res)
	}
}

func TestProbeRealUDPLoopback(t *testing.T) {
	listen := func(rewrite func(*net.UDPAddr) *net.UDPAddr) *stuntest.Server {
		pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		s := stuntest.Serve(pc, rewrite)
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	a := listen(nil)
	b := listen(nil)
	shifted := listen(func(u *net.UDPAddr) *net.UDPAddr { return &net.UDPAddr{IP: u.IP, Port: u.Port + 1} })

	run := func(servers ...string) natprobe.Mapping {
		res, err := natprobe.Probe(context.Background(), servers, natprobe.Options{Timeout: 2 * time.Second, Retransmit: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		return res.Mapping
	}
	if got := run(a.Addr().String(), b.Addr().String()); got != natprobe.Independent {
		t.Fatalf("loopback: %q, want independent", got)
	}
	if got := run(a.Addr().String(), shifted.Addr().String()); got != natprobe.Dependent {
		t.Fatalf("shifted port: %q, want dependent", got)
	}
}

func TestProbeRejectsUnresolvableOnly(t *testing.T) {
	_, err := natprobe.Probe(context.Background(), []string{"not a host"}, natprobe.Options{Timeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a resolve error")
	}
}

func TestHostOnly(t *testing.T) {
	cases := []struct {
		a, b natprobe.Mapping
		want bool
	}{
		{natprobe.Dependent, natprobe.Dependent, true},
		{natprobe.Blocked, natprobe.Independent, true},
		{natprobe.Independent, natprobe.Blocked, true},
		{natprobe.Dependent, natprobe.Independent, false},
		{natprobe.Unknown, natprobe.Dependent, false},
		{natprobe.Independent, natprobe.Independent, false},
	}
	for _, c := range cases {
		if got := natprobe.HostOnly(c.a, c.b); got != c.want {
			t.Errorf("HostOnly(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
