package main

import (
	"log/slog"
	"net"
	"path/filepath"

	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
	"github.com/Hello-CTF/NexTerm/internal/p2p/stunserver"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
)

// startUDPServices starts the STUN responder and the QUIC relay carrier. With the QUIC relay on they share one
// UDP socket per NEXTERM_STUN_LISTEN address; while STUN is off the relay listens on NEXTERM_RELAY_QUIC_LISTEN
// alone. An address that cannot be bound is logged and skipped; the config endpoints advertise only bound
// ports. The returned function stops everything it started.
func startUDPServices(p2p fleetserver.P2PSettings, relay fleetserver.RelaySettings, fleet *fleetserver.Service, dataDir string, logger *slog.Logger) func() {
	if !relay.QUIC {
		stun := startSTUNServer(p2p, fleet, logger)
		return func() {
			if stun != nil {
				_ = stun.Close()
			}
		}
	}
	stunOn := p2p.Enabled && len(p2p.STUNListen) > 0
	addresses := relay.QUICListen
	if stunOn {
		addresses = p2p.STUNListen
	}
	if len(addresses) == 0 {
		return func() {}
	}
	cert, err := relayCertificate(relay, dataDir)
	if err != nil {
		logger.Warn("QUIC relay not started; relay sessions use the websocket carrier", "error", err)
		stun := startSTUNServer(p2p, fleet, logger)
		return func() {
			if stun != nil {
				_ = stun.Close()
			}
		}
	}
	tlsConfig := relaymux.ServerTLSConfig(cert)
	var stun *stunserver.Server
	if stunOn {
		stun = stunserver.New(stunserver.Options{Logger: logger})
	}
	var muxes []*relaymux.UDPMux
	var ports []int
	for _, address := range addresses {
		conn, err := net.ListenPacket("udp", address)
		if err != nil {
			logger.Warn("UDP listener not started; STUN and the QUIC relay skip this address", "address", address, "error", err)
			continue
		}
		mux, err := relaymux.ListenUDPMux(conn, tlsConfig)
		if err != nil {
			logger.Warn("QUIC relay listener not started", "address", address, "error", err)
			continue
		}
		muxes = append(muxes, mux)
		fleet.ServeRelayQUIC(mux)
		if stun != nil {
			if err := stun.Serve(mux.STUNConn()); err != nil {
				logger.Warn("STUN responder not started on the relay socket", "address", address, "error", err)
			}
		}
		ports = append(ports, mux.Addr().(*net.UDPAddr).Port)
		logger.Info("QUIC relay listening", "address", mux.Addr().String(), "stun", stun != nil, "pinned", cert.Pin != "")
	}
	fleet.SetRelayQUIC(ports, cert.Pin)
	if stun != nil {
		fleet.SetP2PSTUNPorts(stun.Ports())
	}
	return func() {
		if stun != nil {
			_ = stun.Close()
		}
		for _, mux := range muxes {
			_ = mux.Close()
		}
	}
}

// relayCertificate is the configured CA-issued certificate, or the self-signed one over the key kept in the
// data directory (a fresh in-memory key without one, so the pin changes on restart).
func relayCertificate(relay fleetserver.RelaySettings, dataDir string) (relaymux.ServerCertificate, error) {
	if relay.CertFile != "" {
		return relaymux.LoadServerCertificate(relay.CertFile, relay.KeyFile)
	}
	if dataDir == "" {
		return relaymux.LoadOrCreatePinnedCertificate("")
	}
	return relaymux.LoadOrCreatePinnedCertificate(filepath.Join(dataDir, fleetserver.RelayQUICKeyFile))
}
