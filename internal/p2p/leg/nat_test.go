package leg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe"
	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe/stuntest"
	"github.com/pion/ice/v4"
	"github.com/pion/transport/v5/vnet"
)

var (
	coneNAT      = &vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointIndependent}
	portRestrict = &vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointAddrPortDependent}
	symmetricNAT = &vnet.NATType{MappingBehavior: vnet.EndpointAddrPortDependent, FilteringBehavior: vnet.EndpointAddrPortDependent}
)

// natTopology puts the client and the agent behind separate NATs on a vnet
// WAN with STUN on two IPs, as the server's config endpoint would advertise.
func natTopology(t testing.TB, clientNAT, agentNAT *vnet.NATType) *stuntest.Topology {
	t.Helper()
	topo, err := stuntest.NewTopology([]string{"1.2.3.4", "1.2.3.5"}, []int{3478},
		stuntest.Site{NAT: clientNAT, PublicIP: "27.1.1.1", CIDR: "192.168.0.0/24", Hosts: []string{"192.168.0.2"}},
		stuntest.Site{NAT: agentNAT, PublicIP: "28.1.1.1", CIDR: "10.2.0.0/24", Hosts: []string{"10.2.0.2"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = topo.Close() })
	return topo
}

func natConfigs(t testing.TB, topo *stuntest.Topology) (Config, Config) {
	c, a := pairConfigs(t)
	c.STUN, a.STUN = topo.STUN, topo.STUN
	c.GatherTimeout, a.GatherTimeout = time.Second, time.Second
	c.iceOptions = []ice.AgentOption{ice.WithNet(topo.Hosts[0][0]), ice.WithCheckInterval(20 * time.Millisecond)}
	a.iceOptions = []ice.AgentOption{ice.WithNet(topo.Hosts[1][0]), ice.WithCheckInterval(20 * time.Millisecond)}
	return c, a
}

func classify(t testing.TB, topo *stuntest.Topology, host *vnet.Net) natprobe.Mapping {
	t.Helper()
	res, err := natprobe.Probe(context.Background(), topo.STUN, natprobe.Options{
		Timeout: 2 * time.Second, Retransmit: 50 * time.Millisecond,
		ListenPacket: host.ListenPacket, ResolveUDPAddr: host.ResolveUDPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.Mapping
}

func TestLegThroughNATs(t *testing.T) {
	cases := []struct {
		name          string
		client, agent *vnet.NATType
		connects      bool
	}{
		{"cone to cone", coneNAT, coneNAT, true},
		{"port-restricted to port-restricted", portRestrict, portRestrict, true},
		{"symmetric to cone", symmetricNAT, coneNAT, true},
		{"symmetric to symmetric", symmetricNAT, symmetricNAT, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := natTopology(t, tc.client, tc.agent)
			c, a := natConfigs(t, topo)
			cm, am := classify(t, topo, topo.Hosts[0][0]), classify(t, topo, topo.Hosts[1][0])
			if natprobe.HostOnly(cm, am) {
				// The selector would only try host candidates here, which
				// cannot cross two NATs.
				if tc.connects {
					t.Fatalf("probe says host-only (%s/%s) for a pair that should connect", cm, am)
				}
				return
			}
			c.CheckTimeout, a.CheckTimeout = 3*time.Second, 3*time.Second
			cs, as := dialPair(t, c, a)
			if !tc.connects {
				if cs.err == nil || as.err == nil {
					t.Fatalf("unexpected success across %s/%s", cm, am)
				}
				return
			}
			if cs.err != nil || as.err != nil {
				t.Fatalf("Dial (%s/%s): %v / %v", cm, am, cs.err, as.err)
			}
			st := cs.leg.Stats()
			if st.Local.Type == "host" && st.Remote.Type == "host" {
				t.Fatalf("host pair selected across NATs: %+v -> %+v", st.Local, st.Remote)
			}
			echoServer(t, as.leg)
			roundTrip(t, cs.leg, []byte("through the nat"))
		})
	}
}

func TestLegSymmetricBothSidesFailsICE(t *testing.T) {
	topo := natTopology(t, symmetricNAT, symmetricNAT)
	c, a := natConfigs(t, topo)
	if cm, am := classify(t, topo, topo.Hosts[0][0]), classify(t, topo, topo.Hosts[1][0]); cm != natprobe.Dependent || am != natprobe.Dependent {
		t.Fatalf("classification %s/%s, want dependent/dependent", cm, am)
	}
	c.CheckTimeout, a.CheckTimeout = time.Second, time.Second
	cs, as := dialPair(t, c, a)
	if !errors.Is(cs.err, ErrICE) || !errors.Is(as.err, ErrICE) {
		t.Fatalf("errs %v / %v, want ErrICE on both sides", cs.err, as.err)
	}
}
