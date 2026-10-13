package main

import (
	"os"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	fleetagent "github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent/purpose"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func init() {
	fleetagent.PurposeHandlerWirer = func(r *fleetagent.Runtime) {
		dial := purpose.BridgeDialerFunc(r.DialPurposeBridge)
		hub := purpose.NewListenHub()
		r.RegisterPurposeHandler(deviceproto.PurposeFile, purpose.NewFileServer(dial).Serve)
		r.RegisterPurposeHandler(deviceproto.PurposeForward, purpose.NewDialServer(dial).Serve)
		r.RegisterPurposeHandler(deviceproto.PurposeListen, purpose.NewListenServer(dial, hub).Serve)
		r.RegisterPurposeHandler(deviceproto.PurposeAccept, purpose.NewAcceptServer(dial, hub).Serve)
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == supervisor.HelperCommand {
		return supervisor.RunHelperCLI(args[1:])
	}
	return fleetagent.RunCLI(args, os.Getenv, os.Stdout, os.Stderr)
}
