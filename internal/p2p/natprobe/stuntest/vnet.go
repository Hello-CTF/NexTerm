package stuntest

import (
	"errors"
	"io"
	"net"
	"strconv"

	"github.com/pion/logging"
	"github.com/pion/transport/v5/vnet"
)

// Site is one network attached to the virtual WAN. With a NAT the hosts sit
// on CIDR behind PublicIP; without one, Hosts are public addresses on the WAN.
type Site struct {
	NAT      *vnet.NATType
	PublicIP string
	CIDR     string
	Hosts    []string
}

// Topology is a running pion vnet with STUN responders on the WAN.
type Topology struct {
	WAN     *vnet.Router
	Hosts   [][]*vnet.Net
	STUN    []string
	servers []*Server
}

var quietLogs = &logging.DefaultLoggerFactory{Writer: io.Discard, DefaultLogLevel: logging.LogLevelDisabled}

// NewTopology starts a WAN with STUN responders on every stunIP x port and
// the given sites.
func NewTopology(stunIPs []string, stunPorts []int, sites ...Site) (*Topology, error) {
	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "0.0.0.0/0", LoggerFactory: quietLogs})
	if err != nil {
		return nil, err
	}
	topo := &Topology{WAN: wan}
	var public *vnet.Net
	if len(stunIPs) > 0 {
		if public, err = vnet.NewNet(&vnet.NetConfig{StaticIPs: stunIPs}); err != nil {
			return nil, err
		}
		if err = wan.AddNet(public); err != nil {
			return nil, err
		}
	}
	for _, site := range sites {
		var hosts []*vnet.Net
		if site.NAT == nil {
			for _, ip := range site.Hosts {
				n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
				if err != nil {
					return nil, err
				}
				if err := wan.AddNet(n); err != nil {
					return nil, err
				}
				hosts = append(hosts, n)
			}
			topo.Hosts = append(topo.Hosts, hosts)
			continue
		}
		lan, err := vnet.NewRouter(&vnet.RouterConfig{
			StaticIPs:     []string{site.PublicIP},
			CIDR:          site.CIDR,
			NATType:       site.NAT,
			LoggerFactory: quietLogs,
		})
		if err != nil {
			return nil, err
		}
		for _, ip := range site.Hosts {
			n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
			if err != nil {
				return nil, err
			}
			if err := lan.AddNet(n); err != nil {
				return nil, err
			}
			hosts = append(hosts, n)
		}
		if err := wan.AddRouter(lan); err != nil {
			return nil, err
		}
		topo.Hosts = append(topo.Hosts, hosts)
	}
	if err := wan.Start(); err != nil {
		return nil, err
	}
	for _, ip := range stunIPs {
		for _, port := range stunPorts {
			addr := net.JoinHostPort(ip, strconv.Itoa(port))
			pc, err := public.ListenPacket("udp4", addr)
			if err != nil {
				_ = topo.Close()
				return nil, err
			}
			topo.servers = append(topo.servers, Serve(pc, nil))
			topo.STUN = append(topo.STUN, addr)
		}
	}
	return topo, nil
}

// Server returns the responder listening on STUN[i].
func (t *Topology) Server(i int) *Server { return t.servers[i] }

// Close stops the responders and the routers.
func (t *Topology) Close() error {
	var errs []error
	for _, s := range t.servers {
		errs = append(errs, s.Close())
	}
	errs = append(errs, t.WAN.Stop())
	return errors.Join(errs...)
}
