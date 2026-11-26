package main

import (
	"log/slog"

	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
	"github.com/Hello-CTF/NexTerm/internal/p2p/stunserver"
)

// startSTUNServer starts the built-in STUN responder on every NEXTERM_STUN_LISTEN address while direct connections
// are on. An address that cannot be bound is logged and skipped, so the HTTP server still starts; the config
// endpoints advertise only the ports actually bound. It returns nil when nothing listens.
func startSTUNServer(settings fleetserver.P2PSettings, fleet *fleetserver.Service, logger *slog.Logger) *stunserver.Server {
	if !settings.Enabled || len(settings.STUNListen) == 0 {
		return nil
	}
	server := stunserver.New(stunserver.Options{Logger: logger})
	for _, address := range settings.STUNListen {
		bound, err := server.Listen(address)
		if err != nil {
			logger.Warn("STUN listener not started; direct connections fall back to the remaining STUN servers", "address", address, "error", err)
			continue
		}
		logger.Info("STUN responder listening", "address", bound.String())
	}
	ports := server.Ports()
	if len(ports) == 0 {
		_ = server.Close()
		return nil
	}
	fleet.SetP2PSTUNPorts(ports)
	return server
}
