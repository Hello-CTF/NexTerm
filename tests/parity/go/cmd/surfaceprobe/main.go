package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	production "github.com/ProbiusOfficial/NexTerm/internal/app/production"
)

type commandSurface struct {
	Commands     []string `json:"commands"`
	PeerCommands []string `json:"peer_commands"`
}

func main() {
	dataDir := flag.String("data-dir", "", "writable data directory for the production composition (required)")
	out := flag.String("out", "", "write the JSON surface to this file instead of stdout")
	flag.Parse()
	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "surfaceprobe: --data-dir is required")
		os.Exit(2)
	}
	application, err := production.NewProduction(context.Background(), production.ProductionConfig{
		Config:          core.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		DataDir:         *dataDir,
		Desktop:         false,
		ForwardPlatform: os.Getenv("NEXTERM_PLATFORM"),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "surfaceprobe: compose production: %v\n", err)
		os.Exit(1)
	}
	peer, err := application.Services.Sync.PeerDispatcher()
	if err != nil {
		fmt.Fprintf(os.Stderr, "surfaceprobe: peer dispatcher: %v\n", err)
		os.Exit(1)
	}
	surface := commandSurface{Commands: application.Dispatcher.Commands(), PeerCommands: peer.Commands()}
	encoded, err := json.MarshalIndent(surface, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "surfaceprobe: encode surface: %v\n", err)
		os.Exit(1)
	}
	encoded = append(encoded, '\n')
	if *out == "" {
		_, _ = os.Stdout.Write(encoded)
	} else if err := os.WriteFile(*out, encoded, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "surfaceprobe: write surface: %v\n", err)
		os.Exit(1)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "surfaceprobe: shutdown: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "surfaceprobe: %d commands, %d peer commands\n", len(surface.Commands), len(surface.PeerCommands))
}
